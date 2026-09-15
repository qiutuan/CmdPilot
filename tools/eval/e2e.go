package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
)

// runE2E drives ≥10 critical paths against the real daemon binary + CLI.
func runE2E() error {
	dir, err := tempDir("e2e")
	if err != nil {
		return err
	}
	defer removeTemp(dir)
	bin, err := buildDaemon(dir)
	if err != nil {
		return err
	}
	base, err := tempDir("e2edata")
	if err != nil {
		return err
	}
	defer removeTemp(base)
	setBaseDir(base)
	dataDir := daemonDataDir(base)

	// mock AI（供 ai test / 混合模式用）
	mockAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"mock-llm","choices":[{"message":{"content":" --ai-suffix"}}]}`))
	}))
	defer mockAI.Close()

	cli := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+base)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	type step struct {
		Name   string
		Ok     bool
		Detail string
	}
	var steps []step
	record := func(name string, ok bool, detail string) {
		steps = append(steps, step{Name: name, Ok: ok, Detail: detail})
	}

	// 1) 启动 → 健康
	if out, err := cli("daemon", "ensure"); err != nil {
		return fmt.Errorf("ensure: %v (%s)", err, out)
	}
	c, _, err := startDaemon(bin, base)
	if err != nil {
		return err
	}
	if ok, msg := c.Health(); ok {
		record("daemon ensure → health", true, msg)
	} else {
		record("daemon ensure → health", false, "health: "+msg)
	}

	// 2) 输入前缀 → 本地建议
	resp, err := c.Complete(client.CompleteReq{Input: "git st", Shell: "cmd", CWD: dataDir})
	record("输入前缀 'git st' → 建议", err == nil && resp.Top != nil, func() string {
		if resp != nil && resp.Top != nil {
			return resp.Top.Full
		}
		return fmt.Sprint(err)
	}())

	// 3) 执行（上报）→ 统计 +1
	if err := c.ReportUsage("git stash", dataDir, "cmd"); err != nil {
		return err
	}
	statsOut, err := cli("stats", "top")
	record("执行上报 → 统计可见", err == nil && strings.Contains(statsOut, "git stash"), strings.ReplaceAll(strings.TrimSpace(statsOut), "\n", "; "))

	// 4) 频率推荐（空输入）
	rec, err := c.Complete(client.CompleteReq{Input: "", Shell: "cmd", CWD: dataDir})
	record("空输入 → 频率推荐", err == nil && len(rec.Recommendations) > 0, func() string {
		if len(rec.Recommendations) > 0 {
			return rec.Recommendations[0].Full
		}
		return fmt.Sprint(err)
	}())

	// 5) 收藏：增 → 参与补全
	if _, err := cli("favorite", "add", "deploy-all", "git push origin main && npm run build", "--tag", "deploy"); err != nil {
		return err
	}
	favResp, err := c.Complete(client.CompleteReq{Input: "deploy", Shell: "cmd", CWD: dataDir})
	record("收藏命令参与补全", err == nil && favResp.Top != nil && favResp.Top.Source == "favorite", func() string {
		if favResp.Top != nil {
			return favResp.Top.Full + " (" + favResp.Top.Source + ")"
		}
		return fmt.Sprint(err)
	}())

	// 6) 配置 set/get
	if _, err := cli("config", "set", "engine", "local"); err != nil {
		return err
	}
	out, err := cli("config", "get", "engine")
	record("配置 set/get", err == nil && strings.Contains(out, "local"), strings.TrimSpace(out))

	// 7) 收藏导出 → 导入合并
	exportPath := filepath.Join(dir, "favorites-export.json")
	if out, err := cli("favorite", "export", exportPath); err != nil {
		return err
	} else if _, statErr := os.Stat(exportPath); statErr != nil {
		return fmt.Errorf("export file missing: %v (%s)", statErr, out)
	}
	if _, err := cli("favorite", "import", exportPath, "--conflict", "skip"); err != nil {
		return err
	}
	record("收藏导出/导入合并", true, exportPath)

	// 8) AI 测试（mock 服务器）
	writeConfig(base, map[string]any{
		"engine": "hybrid",
		"ai": map[string]any{
			"base_url": mockAI.URL, "api_key_encrypted": "FAKE_KEY", "model": "mock-llm",
		},
	})
	c2, _, err := startDaemon(bin, base) // 重新加载配置
	if err != nil {
		return err
	}
	aiOut, err := c2.AITest()
	record("AI 测试连接（mock）", err == nil && strings.Contains(aiOut, "mock-llm"), aiOut)

	// 9) 自检命令输出网络声明
	out, err = cli("self-check")
	record("自检输出网络声明", err == nil && strings.Contains(out, "AI base_url"), strings.TrimSpace(strings.ReplaceAll(out, "\n", "; ")))

	// 10) 重启守护进程 → 数据持久
	if err := c2.Shutdown(); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	c3, _, err := startDaemon(bin, base)
	if err != nil {
		return err
	}
	defer stopDaemon(c3)
	statsOut2, err := cli("stats", "top")
	record("重启后统计持久", err == nil && strings.Contains(statsOut2, "git stash"), strings.ReplaceAll(strings.TrimSpace(statsOut2), "\n", "; "))

	// 11) 统计清空 → 收藏保留
	if _, err := cli("stats", "clear"); err != nil {
		return err
	}
	favList, err := cli("favorite", "list")
	record("清空统计保留收藏", err == nil && strings.Contains(favList, "deploy-all"), strings.ReplaceAll(strings.TrimSpace(favList), "\n", "; "))

	// 12) 无效 AI 配置 → 优雅降级（非崩溃）
	writeConfig(base, map[string]any{
		"engine": "hybrid",
		"ai": map[string]any{
			"base_url": "http://127.0.0.1:1/v1", "api_key_encrypted": "FAKE_KEY", "model": "mock-llm",
		},
	})
	c4, _, err := startDaemon(bin, base)
	if err != nil {
		return err
	}
	badResp, err := c4.Complete(client.CompleteReq{Input: "git st", Shell: "cmd", CWD: dataDir, WaitAIMS: 200})
	record("无效 AI 端点 → 本地兜底", err == nil && badResp != nil && badResp.Top != nil, func() string {
		if badResp != nil && badResp.Top != nil {
			return badResp.Top.Full
		}
		return fmt.Sprint(err)
	}())
	stopDaemon(c4)

	pass := 0
	var rows []string
	for _, s := range steps {
		if s.Ok {
			pass++
		}
		mark := "✅"
		if !s.Ok {
			mark = "❌"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s |", mark, s.Name, s.Detail))
	}

	md := fmt.Sprintf(`# E2E 关键路径自动化测试报告

执行时间：%s
通过 **%d / %d**

| 结果 | 路径 | 证据 |
|---|---|---|
%s

> 运行方式：`+"`go run ./tools/eval e2e`"+`（真实守护进程二进制 + HTTP 客户端 + CLI 子进程）。
`, time.Now().Format("2006-01-02 15:04:05"), pass, len(steps), strings.Join(rows, "\n"))

	jsonData, _ := json.MarshalIndent(map[string]any{
		"pass": pass, "total": len(steps), "steps": steps, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")
	if pass != len(steps) {
		return fmt.Errorf("e2e: %d/%d passed", pass, len(steps))
	}
	return writeReport("e2e", md, jsonData)
}

// writeConfig writes a minimal config JSON for the daemon.
func writeConfig(base string, cfg map[string]any) {
	dataDir := daemonDataDir(base)
	_ = os.MkdirAll(dataDir, 0o755)
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(filepath.Join(dataDir, "config.json"), raw, 0o644)
}

var _ = http.StatusOK
