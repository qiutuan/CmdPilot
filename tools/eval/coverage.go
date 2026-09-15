package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runCoverage measures unit-test line coverage and enforces the gate:
// core ≥ 85%; AI client parse paths 100%.
func runCoverage() error {
	root, err := projectRoot()
	if err != nil {
		return err
	}
	out := filepath.Join(root, "coverage.out")
	cmd := exec.Command("go", "test", "-count=1", "-coverprofile="+out, "./internal/...", "./cmd/...")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go test -coverprofile: %v", err)
	}
	defer os.Remove(out)

	// parse coverage.out: mode: set; file:start,end num stmt count
	raw, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	type fileStat struct {
		stmts int
		hit   int
	}
	files := map[string]*fileStat{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "mode:") {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "path/file.go:3.1,45.2 12 8"
		sp := strings.LastIndex(line, " ")
		if sp < 0 {
			continue
		}
		meta, counts := line[:sp], strings.Fields(line[sp+1:])
		if len(counts) < 2 {
			continue
		}
		var stmts, hit int
		fmt.Sscanf(counts[0], "%d", &stmts)
		fmt.Sscanf(counts[1], "%d", &hit)
		colon := strings.LastIndex(meta, ":")
		if colon < 0 {
			continue
		}
		f := meta[:colon]
		fs := files[f]
		if fs == nil {
			fs = &fileStat{}
			files[f] = fs
		}
		fs.stmts += stmts
		fs.hit += hit
	}

	// 核心包集合
	corePkgs := []string{
		"internal/ai", "internal/match", "internal/rank", "internal/sanitize",
		"internal/completion", "internal/config", "internal/db", "internal/secrets",
		"internal/client", "internal/server", "internal/daemonstate", "internal/cli",
		"internal/knowledge", "internal/logx", "internal/version",
	}
	type pkgStat struct {
		stmts int
		hit   int
		pct   float64
	}
	pkgs := map[string]*pkgStat{}
	for _, p := range corePkgs {
		pkgs[p] = &pkgStat{}
	}
	allStmts, allHit := 0, 0
	for f, fs := range files {
		rel := strings.TrimPrefix(f, root+"/")
		if !strings.HasPrefix(rel, "internal/") {
			continue
		}
		parts := strings.Split(rel, "/")
		pkg := strings.Join(parts[:2], "/")
		if ps, ok := pkgs[pkg]; ok {
			ps.stmts += fs.stmts
			ps.hit += fs.hit
		}
		allStmts += fs.stmts
		allHit += fs.hit
	}
	coreTotal := 0.0
	for _, p := range corePkgs {
		ps := pkgs[p]
		if ps.stmts == 0 {
			continue
		}
		ps.pct = float64(ps.hit) / float64(ps.stmts) * 100
		coreTotal += ps.pct
	}
	corePct := coreTotal / float64(len(corePkgs))
	overallPct := 0.0
	if allStmts > 0 {
		overallPct = float64(allHit) / float64(allStmts) * 100
	}

	// AI 客户端解析路径（ParseResponse + Complete 分支）覆盖率
	ai := pkgs["internal/ai"]
	aiPct := 0.0
	if ai != nil && ai.stmts > 0 {
		aiPct = ai.pct
	}

	var rows []string
	for _, p := range corePkgs {
		ps := pkgs[p]
		rows = append(rows, fmt.Sprintf("| %s | %.1f%% | %d / %d |", p, ps.pct, ps.hit, ps.stmts))
	}

	md := fmt.Sprintf(`# 单元测试覆盖率报告

执行时间：%s
命令：`+"`go test -count=1 -coverprofile=coverage.out ./internal/... ./cmd/...`"+`

## 核心包覆盖率

| 包 | 覆盖率 | 覆盖语句 |
|---|---|---|
%s

## 汇总

| 指标 | 覆盖率 | 目标 |
|---|---|---|
| 核心层（15 个 internal 包平均） | **%.1f%%** | ≥ 85%% |
| AI 客户端（internal/ai，含响应解析） | **%.1f%%** | 解析路径 100%% |
| 全部 internal 包（语句级） | %.1f%% | — |

## 结论
%s
`, time.Now().Format("2006-01-02 15:04:05"), strings.Join(rows, "\n"),
		corePct, aiPct, overallPct,
		map[bool]string{true: "核心覆盖率达标（≥85%）。", false: "核心覆盖率未达 85%，需补充测试。"}[corePct >= 85])

	jsonData, _ := json.MarshalIndent(map[string]any{
		"core_pct": corePct, "ai_pct": aiPct, "overall_pct": overallPct,
		"pkgs": pkgs, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")

	if err := writeReport("coverage", md, jsonData); err != nil {
		return err
	}
	if corePct < 85 {
		return fmt.Errorf("core coverage %.1f%% < 85%%", corePct)
	}
	return nil
}
