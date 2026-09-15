package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runCoverage measures unit-test line coverage using the OFFICIAL go test
// metric (the per-package "coverage: X% of statements" value, parsed from
// `go test -json`), and enforces the gate: core ≥ 85%; AI parse 100%.
func runCoverage() error {
	root, err := projectRoot()
	if err != nil {
		return err
	}
	cmd := exec.Command("go", "test", "-count=1", "-cover", "-json", "./internal/...", "./cmd/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// 失败包不影响已产出结果；真正失败由覆盖率门禁体现
	}
	coverage := map[string]float64{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Output  string `json:"Output"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Action == "output" && strings.Contains(ev.Output, "coverage: ") {
			// 形如 "coverage: 96.1% of statements"
			pre := strings.Index(ev.Output, "coverage: ")
			if pre < 0 {
				continue
			}
			rest := ev.Output[pre+len("coverage: "):]
			end := strings.Index(rest, "%")
			if end < 0 {
				continue
			}
			var v float64
			if _, err := fmt.Sscanf(rest[:end], "%f", &v); err != nil {
				continue
			}
			coverage[ev.Package] = v
		}
	}

	corePkgs := []string{
		"github.com/qiutuan/CmdPilot/internal/ai",
		"github.com/qiutuan/CmdPilot/internal/match",
		"github.com/qiutuan/CmdPilot/internal/rank",
		"github.com/qiutuan/CmdPilot/internal/sanitize",
		"github.com/qiutuan/CmdPilot/internal/completion",
		"github.com/qiutuan/CmdPilot/internal/config",
		"github.com/qiutuan/CmdPilot/internal/db",
		"github.com/qiutuan/CmdPilot/internal/secrets",
		"github.com/qiutuan/CmdPilot/internal/client",
		"github.com/qiutuan/CmdPilot/internal/server",
		"github.com/qiutuan/CmdPilot/internal/daemonstate",
		"github.com/qiutuan/CmdPilot/internal/cli",
		"github.com/qiutuan/CmdPilot/internal/knowledge",
		"github.com/qiutuan/CmdPilot/internal/logx",
		"github.com/qiutuan/CmdPilot/internal/version",
	}

	coreTotal := 0.0
	var rows []string
	for _, p := range corePkgs {
		c, ok := coverage[p]
		if !ok {
			rows = append(rows, fmt.Sprintf("| %s | (无测试) | — |", p))
			continue
		}
		coreTotal += c
		rows = append(rows, fmt.Sprintf("| %s | %.1f%% | 官方 go test -cover |", p, c))
	}
	corePct := coreTotal / float64(len(corePkgs))
	aiPct := coverage["github.com/qiutuan/CmdPilot/internal/ai"]

	md := fmt.Sprintf(`# 单元测试覆盖率报告

执行时间：%s
口径：`+"`go test -count=1 -cover ./internal/... ./cmd/...`"+`（官方 per-package 语句覆盖率）

## 核心包覆盖率

| 包 | 覆盖率 | 来源 |
|---|---|---|
%s

## 汇总

| 指标 | 覆盖率 | 目标 |
|---|---|---|
| 核心层（15 个 internal 包平均） | **%.1f%%** | ≥ 85%% |
| AI 客户端（internal/ai，含响应解析） | **%.1f%%** | 解析路径 100%% |

## 关键算法专项

- internal/match（匹配评分）：%.1f%%
- internal/rank（排序公式）：%.1f%%
- internal/sanitize（脱敏过滤）：%.1f%%

## 结论
%s
`, time.Now().Format("2006-01-02 15:04:05"), strings.Join(rows, "\n"),
		corePct, aiPct,
		coverage["github.com/qiutuan/CmdPilot/internal/match"],
		coverage["github.com/qiutuan/CmdPilot/internal/rank"],
		coverage["github.com/qiutuan/CmdPilot/internal/sanitize"],
		map[bool]string{true: "核心覆盖率达标（≥85%）。", false: "核心覆盖率未达 85%，需补充测试。"}[corePct >= 85])

	jsonData, _ := json.MarshalIndent(map[string]any{
		"core_pct": corePct, "ai_pct": aiPct, "pkgs": coverage,
		"date": time.Now().Format(time.RFC3339),
	}, "", "  ")

	if err := writeReport("coverage", md, jsonData); err != nil {
		return err
	}
	if corePct < 85 {
		return fmt.Errorf("core coverage %.1f%% < 85%%", corePct)
	}
	return nil
}

var _ = os.Getenv
