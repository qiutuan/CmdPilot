# CmdPilot Windows 11 安装与测试报告

> 首次在 Windows 真机（Windows 11 Pro for Workstations 10.0.26100，PowerShell 5.1.26100）安装与测试的完整记录。
> 环境准备：Go 1.27.1（用户级安装），PSReadLine 2.4.5。

---

## 阶段一：环境准备

### 问题 1：机器上没有 Go 工具链
- **现象**：`go: command not found`。
- **处理**：`winget install GoLang.Go --scope user` 失败（exit 16，该包不支持用户级安装，需管理员）。改为从 go.dev 下载官方 zip（75.3 MB）解压到 `%LOCALAPPDATA%\Programs\go`，并把 `bin` 加入**用户级 PATH**（无需管理员）。
- **优化点**：README 应补充"免管理员安装 Go"的明确步骤；CI/安装脚本可预检 `go version` 并给出指引。

### 问题 2：PSReadLine 版本过旧
- **现象**：PowerShell 适配器的 legacy 路径要求 `PSReadLine >= 2.2.0`，系统自带 2.0.0。
- **处理**：`Install-Module PSReadLine -Force -Scope CurrentUser -SkipPublisherCheck` → 2.4.5。
- **优化点**：`install.ps1` 可在写入加载行之前检查 PSReadLine 版本，不足时自动升级或给出命令。

### 问题 3：机器级 `GOROOT` 指向旧的 Go 安装（潜伏环境 bug）
- **现象**：所有包构建失败 `compile: version "go1.23.4" does not match go tool version "go1.27.1"`。
- **根因**：机器级环境变量 `GOROOT=D:\Program Files\Go` 指向旧 Go 1.23.4（其 `bin` 不在 PATH，因此平时发现不了）。新装 Go 的二进制被该 GOROOT 劫持，去调用旧版本的标准库/编译器。
- **处理**：在**用户级**覆盖 `GOROOT=%LOCALAPPDATA%\Programs\go`（用户级优先于机器级，可逆，无需管理员），会话内清空进程 GOROOT 验证 `go env GOROOT` 正确推导。测试与会话结束后，普通命令不受影响。
- **优化点**：此类环境问题项目无法控制，但可在 `install.ps1` 的构建前做一次 `go env GOROOT` 与 `go version` 一致性自检，给出明确报错而非神秘的版本不匹配。

---

## 阶段二：构建与单元测试（首次运行，暴露 3 个真实问题）

### 问题 4：`install.ps1` 解析失败（真实项目 bug）
- **现象**：PowerShell 报 `Unexpected token '}'`，第 48 行。
- **根因**：仓库内**全部 7 个** `.ps1/.psm1/.psd1` 均为**无 BOM 的 UTF-8**。Windows PowerShell 5.1（Windows 11 自带）对无 BOM 文件按系统 ANSI 代码页（中文系统为 GBK）读取，中文乱码导致字符串/括号解析错乱。用显式 UTF-8 读取时 0 错误，默认读取 1 错误，实证确认。
- **修复**：7 个 PowerShell 文件统一前置 UTF-8 BOM（内容字节原样保留，仅加 3 字节前缀）。提交 `7f65287`。
- **优化点**：建议补充 `.editorconfig`（`charset = utf-8-bom`）与提交前脚本检查，防止再次引入无 BOM 文件；PowerShell 7 默认 UTF-8 无此问题，但 5.1 是 Windows 默认宿主，必须兼容。

### 问题 5：`logx` 日志轮转在 Windows 上失效（真实生产 bug）
- **现象**：`TestRotate` 失败，当前日志文件达 43890 字节未轮转。
- **根因**：`rotate()` 先 `open()` 重新持有文件句柄再 `os.Rename`。Windows **无法重命名打开中的文件**（`The process cannot access the file because it is being used by another process`，已用最小复现实证），而 `os.Rename` 的错误被 `//nolint:errcheck` 静默吞掉 → `rotate()` 仍返回 nil → 日志无限写进同一个文件，永不轮转。对守护进程的长期日志等于失效。
- **修复**：先关闭句柄 → 重命名（**检查关键 rename 错误**，失败回滚重开当前文件）→ 重开新文件。提交 `516777e`。
- **优化点**：全库应审计 `//nolint:errcheck` 吞错的位置（失败静默化是最危险的一类）；logx 应有 Windows 平台上的轮转回归测试。

### 问题 6：补全引擎测试在 Windows 失败（测试可移植性 bug）
- **现象**：`TestPathCompletion` 路径列表为空；`TestGitRepoDetectionAncestor` 祖先 git 仓库未检测到。
- **根因**：`fakeFS` 以 POSIX 风格 `"/proj/src"` 作为 map 键，而引擎内部用 `filepath.Join/Dir` 构造路径——Windows 上产出反斜杠 `"\proj\src"` → 键不匹配 → 查不到目录 / 找不到祖先 `.git`。该测试套件从未在 Windows 上跑过。
- **修复**：`fakeFS` 所有路径键统一经 `fsKey = filepath.ToSlash(filepath.Clean(...))` 规范化（正斜杠形式），跨平台一致。提交 `6785f14`。
- **优化点**：CI 应包含 Windows runner（本项目以 Windows 为目标平台，却从未在 Windows 验证过测试）；生产路径补全逻辑本身在 Windows 正常（`osFS` 直接用 OS 原生路径），仅测试基建有平台假设。

### 修复后
`go test -count=1 ./...` **17 个包全部通过**。

---

## 阶段三：安装执行

- `.\installer\install.ps1` 运行成功（exit 0）：构建 `cmdpilot.exe`（11.9 MB）与 `cmdpilot-clink.exe`（10.8 MB）→ 安装 PS 模块到 `$HOME\Documents\PowerShell\Modules` 与 `WindowsPowerShell\Modules` → 复制 Clink Lua 插件到 `%LOCALAPPDATA%\clink` → 幂等写入 `$PROFILE` 加载行。
- **问题 7（显示瑕疵）**：`Write-Step "写入 \$PROFILE 自动加载"` 展开的是 `$PROFILE`（当前宿主路径 `Microsoft.PowerShell_profile.ps1`），而实际写入的是 `$PROFILE.CurrentUserAllHosts`（`profile.ps1`），输出有误导性。
- **优化点（潜在 bug）**：`install.ps1` 的 `.DESCRIPTION` 与第 116 行引用了 `-DataDir / $DataDir`，但 `param` 块从未声明该参数（`$DataDir` 恒为 null，分支永不生效）。应补齐声明或删除占位代码。

---

## 阶段四：功能测试

| 项 | 结果 |
|---|---|
| `cmdpilot version` | `CmdPilot 0.1.0` ✓ |
| `cmdpilot self-check` | 知识库 496 条（cmd=179 ps=224 external=93）、DB 完整性 ok ✓ |
| `complete --input "git st" --shell cmd` | `git stash/status/switch/reset`，git 仓库检测 ✓ |
| `daemon ensure/status/stop` | detached 后台启动、健康状态 ✓ |
| `report` → `stats top 5` | 使用记录入库，"越用越懂你"频率排序 ✓ |
| `/complete` bearer 认证 | 无 token → 401 ✓ |

- **问题 8（易用性）**：`daemon start` 是**前台**运行（`select{}` 阻塞），正确后台启动命令是 `daemon ensure`（内部经 `startDaemonDetached` 分离子进程）。首次测试误用 `start` 导致命令被超时连带杀掉。
- **优化点**：`daemon start` 对交互用户有误导，可考虑直接走 detached 或改为 `ensure` 语义；`startDetached` 在 Windows 上可用 `CREATE_NO_WINDOW` 标志避免弹出控制台窗口。

---

## 阶段五：AI 模块配置（DeepSeek）

- 配置：`ai.base_url = https://api.deepseek.com`、`ai.model = deepseek-chat`、`ai.api_key`（**DPAPI 加密**存储为 `api_key_encrypted`，配置文件中无明文）。
- `cmdpilot ai test`：**连接成功，model: deepseek-flash**。
- 端到端 `/complete`（重启 daemon 加载 AI 配置后）：
  - 第 1 次请求：本地兜底 `git commit -am`（`ai_used=false`，AI 异步抓取中）；
  - 3 秒后第 2 次请求：**命中 AI 缓存，`ai_used=true`**，AI 生成补全，source=ai。
- **优化点**：AI 生成质量参差（如对 `git commit -a` 生成 `git commit -a-m ""`），可加强 `ValidateSuffix` 约束（如阻止多余 `-m ""` 空参、对子命令候选过滤）；CLI 的 `complete` 为单次进程、看不到 daemon 内的 AI 缓存，调试 AI 建议直连 `/complete` 端点。

---

## Git 管理

按功能/模块拆分为 4 个聚焦提交（非一次性大提交），已推送到 `origin/main`：

| 提交 | 内容 |
|---|---|
| `89e6c9f` | `docs:` 新增 CLAUDE.md（构建/测试命令、三进程架构、AI 请求流、提交规范） |
| `7f65287` | `fix(scripts):` PowerShell 脚本加 UTF-8 BOM，兼容 PS 5.1 中文解析 |
| `516777e` | `fix(logx):` 日志轮转先关闭句柄再重命名并检查错误（Windows 修复） |
| `6785f14` | `test(completion):` fakeFS 路径键 ToSlash 规范化，修复 Windows 测试 |

GitHub 认证凭据经 **Windows 凭据管理器（GCM）** 存储，未以明文写入仓库或配置文件。

---

## 总结

首次在 Windows 上安装运行共发现并修复 **3 个真实 bug**（PS 脚本编码、logx 轮转、测试可移植性）+ **2 个环境问题**（Go/GOROOT、PSReadLine），并整理 **8 处优化点**。安装、功能测试、DeepSeek AI 配置全部完成并通过。
