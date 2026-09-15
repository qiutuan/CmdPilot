# CmdPilot

**Windows 命令行智能补全助手** —— 轻量级、双引擎（本地知识库兜底 + OpenAI 兼容 AI 增强）、越用越懂你。

为 Windows 下重度使用 CMD / PowerShell 的开发、测试、运维人员提供 fish/Copilot 式的内联建议：

> 本地库匹配兜底 + OpenAI 协议 AI 增强 + 频率统计推荐 + 幽灵文本补全

[![Go](https://img.shields.io/badge/Go-1.22%2B-blue)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

---

## 特性

- **双终端**：PowerShell（PSReadLine Predictor，PS 5.1 + PS 7.x）与 CMD（Clink 插件）
- **本地知识库**：492 条内置命令（CMD 179 / PowerShell 224 / 外部工具 93，含参数、示例、标签）
- **三级匹配**：前缀 → fzf 风格子串/模糊（连续子串、词首字母加分）→ 编辑距离 ≤2 容错
- **上下文感知**：命令名/子命令/参数/路径分状态补全；git 仓库检测；链式推荐（`git add` → `git commit`）
- **AI 增强（可选）**：OpenAI 兼容协议，key 用 Windows DPAPI 加密存储，300ms debounce，全程异步不阻塞输入
- **越用越懂你**：`score = log(1+次数) × 0.5^(天数/30) × 上下文加成(×1.5)`，收藏加权高于历史
- **收藏与片段**：占位符 `{{param}}`、JSON/CSV 导入导出、冲突策略（skip/overwrite/rename）
- **绝对可靠**：AI 超时/失败/未配置 → 自动降级本地 → 退化为历史前缀匹配，终端永不卡输入
- **隐私**：全部本地存储、无网络遥测；`cmdpilot self-check` 可查证唯一出网请求为 AI base_url
- **轻量**：本地模式常驻内存 ≤30MB、空闲 CPU ≈0%（事件驱动）、本地补全 P95 ≤10ms（实测见报告）

## 快速开始

### 前置

- Windows 10/11；PowerShell 5.1 或 7.x（PSReadLine ≥2.2）；CMD 需 Clink（未安装时安装器会引导，不报错退出）
- Go ≥1.22（仅构建需要，运行不需要）

### 安装（免管理员，CurrentUser）

```powershell
# 1. 克隆
git clone https://github.com/qiutuan/CmdPilot.git
cd CmdPilot

# 2. 一键安装
powershell -ExecutionPolicy Bypass -File installer\install.ps1

# 3. 新开 PowerShell 窗口即生效；CMD 侧安装 Clink 后自动加载
```

安装器会自动：构建二进制 → 安装 PowerShell 模块 → 复制 Clink 插件 → 写入 `$PROFILE` 自动加载。

### 配置 AI（可选，不配则纯本地模式）

```powershell
cmdpilot config set ai.base_url https://api.openai.com/v1
cmdpilot config set ai.api_key sk-...     # DPAPI 加密
cmdpilot config set ai.model gpt-4o-mini
cmdpilot ai test                          # 验证连接
```

### 卸载

```powershell
powershell -ExecutionPolicy Bypass -File installer\uninstall.ps1   # 保留数据
powershell -ExecutionPolicy Bypass -File installer\uninstall.ps1 -RemoveData
```

## 使用

| 场景 | 操作 |
|---|---|
| 内联幽灵文本建议（PS） | 直接输入，灰色建议出现后按 **Tab** 或 **→** 接受，**Esc** 忽略 |
| 列表候选（PS / CMD） | Tab 打开候选列表，方向键选择，Enter/Tab 接受，Esc 取消 |
| 仅 Tab 触发 AI | `cmdpilot config set trigger tab` |
| 收藏命令 | `cmdpilot favorite add --name deploy --command "git push && npm run build" --tags ci` |
| 统计 | `cmdpilot stats top 10` / `stats trend` / `stats distribution` / `stats clear` |
| 测试补全（脱离终端） | `cmdpilot complete --input "git st" --shell cmd --json` |

> Clink 无自动建议（幽灵文本）API，CMD 侧以 **Tab 菜单补全**为最佳替代：Enter/Tab 接受、Esc 取消。

## 构建与测试

```powershell
# 构建（Linux/macOS 交叉构建示例）
go build ./cmd/cmdpilot ./cmd/cmdpilot-clink

# 全量单元测试
go test -count=1 ./...

# 七合一评测（引擎命中率/覆盖率/降级矩阵/脱敏/稳定性/E2E/性能）
go run ./tools/eval all
# 产物：docs/reports/*.{md,json}
```

## 测试报告摘要（docs/reports/）

| 报告 | 结果 |
|---|---|
| 引擎质量（231 条真实场景） | **Top-5 100%**（231/231），Top-1 97.4% |
| 单元测试覆盖率 | 核心层平均 **85.1%**；AI 解析 100%；match 96.1% / rank 96.2% / sanitize 100% |
| 降级矩阵（5 故障） | 超时/500/断网/无效 key/畸形响应 → 全部静默降级本地，终端可用 |
| 脱敏 | 7 种敏感样本注入历史 → mock 捕获请求体 **0 泄漏** |
| 稳定性 | 并发写入中 kill -9 → integrity ok、数据可读、守护进程可重启 |
| E2E | 12/12 关键路径通过 |
| 性能 | 本地补全 P95 ≤10ms、万条历史延迟、RSS ≤30MB、空闲 CPU≈0%、AI 异步非阻塞（AI 慢 1.2s 首返 ≤500ms） |

## 文档

- [架构文档](docs/architecture.md)
- [ADR（决策记录）](docs/adr/)：终端接入选型 / 双引擎融合与降级 / 排序公式 / key 安全存储 / 上下文感知 / 存储格式
- [CLI 文档](docs/cli.md)
- [配置说明](docs/configuration.md)

## 目录结构

```
adapters/powershell   PSReadLine Predictor 插件（New + Legacy 双 API）
adapters/clink        Clink Lua 插件（CMD）
cmd/cmdpilot          守护进程（常驻 HTTP 服务）
cmd/cmdpilot-clink    companion 伴侣进程（JSON + 行协议）
internal/             核心层（completion/ai/match/rank/sanitize/db/config/secrets/knowledge/...）
tools/gendb           内置知识库生成器
tools/eval            七合一评测工具
installer/            一键安装/卸载脚本
docs/                 架构/ADR/CLI/配置/测试报告
```

## 隐私与安全

- 统计、收藏、配置、日志全部本地；无任何网络遥测
- AI key：Windows DPAPI 加密存储；历史脱敏（含 key/token/password/secret/Bearer 的行绝不进 AI prompt，mock 捕获验证 0 泄漏）
- 守护进程仅监听 127.0.0.1，Bearer token 鉴权
- `cmdpilot self-check` 输出本工具发起的唯一网络请求仅为用户配置的 AI base_url

## 许可证

MIT
