package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

func pct(arr []time.Duration, p float64) time.Duration {
	if len(arr) == 0 {
		return 0
	}
	s := make([]time.Duration, len(arr))
	copy(s, arr)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	return s[idx]
}

type perfMetric struct {
	P50   string
	P95   string
	P99   string
	Avg   string
	Extra string
}

func runPerf() error {
	rows := map[string]perfMetric{}
	notes := []string{}
	passMap := map[string]bool{}

	// ---- 1) 本地补全延迟（引擎直测，空历史库） ----
	kb, err := knowledge.Load()
	if err != nil {
		return err
	}
	dir1, err := tempDir("perf")
	if err != nil {
		return err
	}
	defer removeTemp(dir1)
	store, err := db.Open(dir1)
	if err != nil {
		return err
	}
	cfg := config.Default()
	cfg.Engine = config.EngineLocal
	eng := completion.New(kb, store, cfg, nil, nil)

	inputs := []string{
		"git st", "git co", "npm i", "docker ps", "Get-Process", "dir", "ipconfig",
		"kubectl get", "pip install", "go run", "ssh-keygen", "Get-Service",
		"tasklist", "chkdsk", "netstat", "npm run", "docker build", "git push",
	}
	var lat []time.Duration
	const warm = 20
	for i := 0; i < warm; i++ {
		_, _ = eng.LocalComplete(completion.Request{Input: inputs[i%len(inputs)], Shell: "cmd", CWD: dir1})
	}
	const n = 500
	for i := 0; i < n; i++ {
		t0 := time.Now()
		_, _ = eng.LocalComplete(completion.Request{Input: inputs[i%len(inputs)], Shell: "cmd", CWD: dir1})
		lat = append(lat, time.Since(t0))
	}
	rows["本地补全延迟（引擎直测,500 样本）"] = perfMetric{
		P50: pct(lat, 0.50).String(), P95: pct(lat, 0.95).String(),
		P99: pct(lat, 0.99).String(), Avg: avgDur(lat).String(),
		Extra: fmt.Sprintf("样本数 %d", n),
	}
	passMap["本地补全 P95 ≤ 10ms"] = pct(lat, 0.95) <= 10*time.Millisecond
	if !passMap["本地补全 P95 ≤ 10ms"] {
		notes = append(notes, fmt.Sprintf("本地 P95=%.2fms 超过 10ms 硬指标", float64(pct(lat, 0.95))/float64(time.Millisecond)))
	}

	// ---- 2) 万条历史下的补全延迟 ----
	dir2, err := tempDir("perf10k")
	if err != nil {
		return err
	}
	defer removeTemp(dir2)
	store2, err := db.Open(dir2)
	if err != nil {
		return err
	}
	now := time.Now()
	for i := 0; i < 10000; i++ {
		cmd := fmt.Sprintf("command-%d", i)
		if i%97 == 0 {
			cmd = "git commit"
		}
		if err := store2.RecordUsage(db.UsageRecord{Command: cmd, Dir: dir2, Shell: "cmd", At: now.Add(-time.Duration(i) * time.Minute)}); err != nil {
			store2.Close()
			return err
		}
	}
	eng2 := completion.New(kb, store2, cfg, nil, nil)
	var lat2 []time.Duration
	for i := 0; i < 300; i++ {
		t0 := time.Now()
		_, _ = eng2.LocalComplete(completion.Request{Input: "git c", Shell: "cmd", CWD: dir2})
		lat2 = append(lat2, time.Since(t0))
	}
	store2.Close()
	rows["万条历史下补全延迟（git c,300 样本）"] = perfMetric{
		P50: pct(lat2, 0.50).String(), P95: pct(lat2, 0.95).String(),
		P99: pct(lat2, 0.99).String(), Avg: avgDur(lat2).String(),
		Extra: "usage_stats 10000 行",
	}

	// ---- 3) 守护进程常驻内存 + 空闲 CPU（事件驱动，无轮询） ----
	bin, err := buildDaemon(dir1)
	if err != nil {
		return err
	}
	base3, err := tempDir("perfdaemon")
	if err != nil {
		return err
	}
	defer removeTemp(base3)
	setBaseDir(base3)
	c, _, err := startDaemon(bin, base3)
	if err != nil {
		return err
	}
	pid, rssKB, cpuPct, err := probeProcess(bin, base3)
	if err != nil {
		stopDaemon(c)
		return err
	}
	rows["守护进程常驻内存（本地引擎模式）"] = perfMetric{Extra: fmt.Sprintf("RSS %.1f MB (PID %d) — 硬指标 ≤30MB", float64(rssKB)/1024, pid)}
	passMap["常驻内存 ≤ 30MB"] = rssKB <= 30*1024
	if !passMap["常驻内存 ≤ 30MB"] {
		notes = append(notes, fmt.Sprintf("RSS %.1fMB 超过 30MB 硬指标", float64(rssKB)/1024))
	}
	rows["守护进程空闲 CPU（10s 采样）"] = perfMetric{Extra: fmt.Sprintf("%.3f%%（事件驱动，无轮询）— 硬指标 ≈0%%", cpuPct)}
	passMap["空闲 CPU ≈ 0%"] = cpuPct <= 0.5
	if !passMap["空闲 CPU ≈ 0%"] {
		notes = append(notes, fmt.Sprintf("空闲 CPU %.3f%% 偏高", cpuPct))
	}
	stopDaemon(c)

	// ---- 4) AI debounce 非阻塞（mock 慢 AI 1.2s） ----
	slowAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" --slow"}}]}`))
	}))
	defer slowAI.Close()
	base4, err := tempDir("perfai")
	if err != nil {
		return err
	}
	defer removeTemp(base4)
	cfgAI := config.Default()
	cfgAI.Engine = config.EngineHybrid
	cfgAI.Trigger = config.TriggerAuto
	cfgAI.AI.BaseURL = slowAI.URL
	cfgAI.AI.APIKeyEncrypted = "FAKE_KEY"
	cfgAI.AI.Model = "mock"
	setBaseDir(base4)
	if err := cfgAI.Save(); err != nil {
		return err
	}
	c4, _, err := startDaemon(bin, base4)
	if err != nil {
		return err
	}
	t0 := time.Now()
	resp, err := c4.Complete(client.CompleteReq{Input: "git comm", Shell: "cmd", CWD: base4, WaitAIMS: 0})
	firstDur := time.Since(t0)
	if err != nil {
		stopDaemon(c4)
		return fmt.Errorf("complete: %v", err)
	}
	time.Sleep(2500 * time.Millisecond) // 等异步 AI 完成并缓存
	resp2, _ := c4.Complete(client.CompleteReq{Input: "git comm", Shell: "cmd", CWD: base4, WaitAIMS: 0})
	stopDaemon(c4)
	rows["AI debounce 非阻塞（mock AI 延迟 1.2s）"] = perfMetric{
		P50: firstDur.String(), P95: firstDur.String(), P99: firstDur.String(),
		Avg: firstDur.String(),
		Extra: fmt.Sprintf("wait_ai_ms=0 首次返回 %.0fms（远小于 AI 1.2s），ai_used=%v；稍后命中缓存 ai_used=%v",
			float64(firstDur)/float64(time.Millisecond), resp.AIUsed, resp2.AIUsed),
	}
	passMap["AI 全程异步不阻塞"] = firstDur <= 500*time.Millisecond
	if !passMap["AI 全程异步不阻塞"] {
		notes = append(notes, "AI 等待超过 500ms：非阻塞要求未满足")
	}

	// ---- 渲染报告 ----
	var checks []string
	checkOrder := []string{"本地补全 P95 ≤ 10ms", "常驻内存 ≤ 30MB", "空闲 CPU ≈ 0%", "AI 全程异步不阻塞"}
	for _, k := range checkOrder {
		v := "✅ 达标"
		if !passMap[k] {
			v = "❌ 未达标"
		}
		checks = append(checks, fmt.Sprintf("| %s | %s |", k, v))
	}

	md := fmt.Sprintf(`# 性能实测报告

执行时间：%s

## 指标汇总

| 指标 | P50 | P95 | P99 | 平均 | 附注 |
|---|---|---|---|---|---|
%s

## 硬指标核验

| 硬指标 | 结果 |
|---|---|
%s

## 备注
%s

> 守护进程为事件驱动（HTTP/信号），空闲时无定时轮询；内存为 RSS（含 WAL 缓冲）。
> 一键复跑：`+"`go run ./tools/eval perf`"+`
`, time.Now().Format("2006-01-02 15:04:05"),
		reportRows(rows), strings.Join(checks, "\n"),
		func() string {
			if len(notes) == 0 {
				return "（无）"
			}
			return strings.Join(notes, "\n- ")
		}())

	jsonData, _ := json.MarshalIndent(map[string]any{
		"local_p95": pct(lat, 0.95).String(), "local_p99": pct(lat, 0.99).String(),
		"rss_mb": float64(rssKB) / 1024, "idle_cpu_pct": cpuPct,
		"ai_first_ms": float64(firstDur) / float64(time.Millisecond),
		"checks":      passMap, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")
	for _, k := range checkOrder {
		if !passMap[k] {
			return fmt.Errorf("perf gate failed: %s", k)
		}
	}
	return writeReport("performance", md, jsonData)
}

func avgDur(a []time.Duration) time.Duration {
	var t time.Duration
	for _, d := range a {
		t += d
	}
	if len(a) == 0 {
		return 0
	}
	return t / time.Duration(len(a))
}

func reportRows(rows map[string]perfMetric) string {
	var b strings.Builder
	for _, k := range sortedKeys(rows) {
		r := rows[k]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", k, r.P50, r.P95, r.P99, r.Avg, r.Extra)
	}
	return b.String()
}

// probeProcess measures RSS + 10s idle CPU of the daemon.
func probeProcess(bin, base string) (int, int, float64, error) {
	raw, err := os.ReadFile(filepath.Join(daemonDataDir(base), "daemon.json"))
	if err != nil {
		return 0, 0, 0, err
	}
	var st struct {
		PID int `json:"PID"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return 0, 0, 0, err
	}
	pid := st.PID
	stat1, err := procStat(pid)
	if err != nil {
		return 0, 0, 0, err
	}
	time.Sleep(10 * time.Second)
	stat2, err := procStat(pid)
	if err != nil {
		return 0, 0, 0, err
	}
	rssKB, err := procRSS(pid)
	if err != nil {
		return 0, 0, 0, err
	}
	cpu := float64(stat2-stat1) / float64(10*time.Second) * 100
	return pid, rssKB, cpu, nil
}

func procStat(pid int) (int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(raw)
	idx := strings.LastIndexByte(s, ')')
	rest := strings.Fields(s[idx+2:])
	if len(rest) < 14 {
		return 0, fmt.Errorf("short proc stat")
	}
	utime, _ := strconv.ParseInt(rest[11], 10, 64)
	stime, _ := strconv.ParseInt(rest[12], 10, 64)
	return utime + stime, nil
}

func procRSS(pid int) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return strconv.Atoi(fields[1])
			}
		}
	}
	return 0, fmt.Errorf("VmRSS not found")
}
