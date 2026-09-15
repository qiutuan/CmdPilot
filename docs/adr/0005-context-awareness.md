# ADR-005：上下文感知补全设计

- 状态：已接受
- 日期：2026-09-15

## 背景
同一前缀在不同上下文应给出不同建议（git 后补子命令、参数位补参数、
路径位补文件系统）。

## 决策
1. **输入状态机**（internal/completion/engine.go `analyzeInput`）：
   - 命令名位（首词）→ 命令候选（knowledge+favorites+user_commands+历史）
   - 子命令位（如 `git ` 之后）→ 该命令的子命令表
   - 参数位（`-`/`/` 开头）→ 知识库参数表（params 字段）
   - 路径位（含 `/`、`\`、`.`、`~` 等）→ 文件系统补全（按 shell 平台规则）
2. **仓库感知**：自 CWD 向上探测 `.git`，是 git 仓库时增强 git 子命令权重
   并推荐 `git status` 类高频链。
3. **链式推荐**：执行过 `git add` 后优先建议 `git commit`（见 ADR-003）。
4. **shell 相关**：cmd/ps 各自只出各自内建；external 工具（git/npm/pip/
   docker/ssh 等）两终端通用。

## 理由
- 状态机把"该补什么"的语义显式化，避免把所有输入都当命令名匹配；
- 文件系统补全走 OS API，路径体验与原生一致。

## 后果
- 评测集 231 条含命令名/子命令/参数/大小写/编辑距离容错场景，
  本地引擎 Top-5 命中率 100%（见 docs/reports/engine-eval.md）。
