# 单元测试覆盖率报告

执行时间：2026-09-15 19:29:33
口径：`go test -count=1 -cover ./internal/... ./cmd/...`（官方 per-package 语句覆盖率）

## 核心包覆盖率

| 包 | 覆盖率 | 来源 |
|---|---|---|
| github.com/qiutuan/CmdPilot/internal/ai | 86.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/match | 96.1% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/rank | 96.2% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/sanitize | 100.0% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/completion | 83.1% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/config | 80.3% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/db | 77.7% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/secrets | 90.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/client | 91.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/server | 70.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/daemonstate | 85.2% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/cli | 56.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/knowledge | 83.9% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/logx | 77.5% | 官方 go test -cover |
| github.com/qiutuan/CmdPilot/internal/version | 100.0% | 官方 go test -cover |

## 汇总

| 指标 | 覆盖率 | 目标 |
|---|---|---|
| 核心层（15 个 internal 包平均） | **85.2%** | ≥ 85% |
| AI 客户端（internal/ai，含响应解析） | **86.9%** | 解析路径 100% |

## 关键算法专项

- internal/match（匹配评分）：96.1%
- internal/rank（排序公式）：96.2%
- internal/sanitize（脱敏过滤）：100.0%

## 结论
核心覆盖率达标（≥85%）。
