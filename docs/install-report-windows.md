# CmdPilot Windows 11 安装与测试报告

> 首次在 Windows 真机（Windows 11 Pro for Workstations 10.0.26100，PowerShell 5.1.26100）安装与测试的完整记录。
> 环境准备：Go 1.27.1（用户级安装）；PSReadLine 最终为 **2.2.5**（初始装 2.4.5，后按下文实证改为 2.2.5）。

---

## 阶段一：环境准备

### 问题 1：机器上没有 Go 工具链
- **现象**：`go: command not found`。
- **处理**：`winget install GoLang.Go --scope user` 失败（exit 16，该包不支持用户级安装，需管理员）。改为从 go.dev 下载官方 zip（75.3 MB）解压到 `%LOCALAPPDATA%\Programs\go`，并把 `bin` 加入**用户级 PATH**（无需管理员）。
- **优化点**：README 应补充"免管理员安装 Go"的明确步骤；CI/安装脚本可预检 `go version` 并给出指引。

### 问题 2：PSReadLine 版本过旧
- **现象**：CmdPilot 的 PS 5.1 路径要求 `PSReadLine >= 2.2.0`（Tab 补全依赖其静态 API），系统自带 2.0.0。
- **处理**：`Install-Module PSReadLine -Force -Scope CurrentUser -SkipPublisherCheck` → 2.4.5；后经实证修正（见"问题 2 补记"）改为安装 **2.2.5**。
- **优化点**：`install.ps1` 可在写入加载行之前检查 PSReadLine 版本，不足时自动升级或给出命令。
- **补记（2026-09-16）**：原以为 PS 5.1 可用 PSReadLine 2.2.x 的"经典 ICommandPredictor"插件 API 实现 inline 幽灵文本。按 **2.2.5 二进制与官方源码实证**：该类型/`RegisterPredictor` 并不存在，且 `-PredictionSource Plugin` 在 .NET Framework 上直接抛 `PredictionPluginNotSupported`。结论：**PS 5.1 上任何 PSReadLine 版本都没有插件预测器 API**，inline 幽灵文本仅 PowerShell 7.4+ 可用；PS 5.1 使用 Tab 补全降级（已实测：`Import-Module CmdPilot` 无报错，Tab 返回本地/AI 补全）。

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

## 补记（2026-09-16）：安装 PowerShell 7 实现 inline 幽灵文本

- **背景**：按"问题 2 补记"实证，PS 5.1 无任何插件预测 API，inline 幽灵文本仅
  PowerShell 7.4+ 的引擎级 Subsystem API 可用（`ICommandPredictor`）。
- **安装**：winget/MSI 安装失败（exit 34，`0x80070422`：msiserver 服务被禁用），
  改用官方 zip 解压到 `%LOCALAPPDATA%\Programs\PowerShell\7.6.6`，追加到用户级
  PATH；创建 PS7 all-hosts profile（`Documents\PowerShell\profile.ps1`），镜像
  PS5.1 的 `Import-Module CmdPilot -ErrorAction SilentlyContinue`。
- **发现并修复探测 bug**：模块原用 `[type]::GetType('...ICommandPredictor')`
  探测引擎 API。实测 pwsh 7.6.6 下该接口明明存在于 System.Management.Automation.dll
  却返回 `$null` —— `Type.GetType(string)` 只搜索**调用程序集与核心库**，不扫
  全部已加载程序集，导致恒判 `none`、inline 永不启用。改为 PowerShell 类型字面量
  （解析器扫描全部已加载程序集）：7.6.6 命中 `new`，5.1 抛"无法找到类型"被捕获
  仍为 `none`。提交 `77f88f2`。
- **端到端验证（pwsh 7.6.6）**：`ApiKind=new` → `SubsystemManager` 注册成功 →
  注册实例 `GetSuggestion` 对 `git st` 返回 top=`git status`（幽灵文本 "atus"）
  + 6 条列表；`PS 5.1` 回归无损（`none` + Tab 降级，零报错）。

---

## 补记（2026-09-16 二次）：幽灵文本可用后的 Tab 失效与卡顿

**现象**：幽灵文本已出现，但 ①按 Tab 无反应 ②窗口变卡、输入延迟高。

### 1. 卡顿根因——worker 空转重取（提交 `97fd6ec`）

预测器后台 worker 每 150ms 轮询输入行，判断依据是"`gen`/`pending` 是否变化"。
但**写完快照并不改变这两个量**，于是"输入没变"成立 → 对**同一个输入**再起一次
companion 进程，永远停不下来。实测：空闲 6 秒内创建 **18 个** `cmdpilot-clink.exe`
（每次约 22ms 进程创建 + 两个临时文件 + Defender 扫描），这就是输入延迟的来源。
加 `$lastGen` 记账（每代只取一次，取前先记账以免失败重试风暴）后，
同样条件下 **6 秒内 1 次**。

### 2. Tab 语义——"有则接受，无则菜单"（提交 `bb40821`，**二次修正 `55f99c3`**）

原实现把 Tab 固定绑 `MenuComplete`：只弹候选列表、不落字，对灰色建议等于"无效"，
与本模块 README 承诺的"按 Tab 或 → 接受"不一致。改为"有内联建议则接受、否则退回
原生菜单"。

**第一版（`bb40821`）走了弯路**：接受动作由本模块自己做（读快照 → `Insert` 后缀），
为此加了 `PeekSuggestion`。但鼠标键盘实测仍"Tab 无效"，按 Tab 得到 PSReadLine 的
`Display all {0} possibilities? (y or n) _`（该串出自 PSReadLine 资源
`get_DisplayAllPossibilities`，是 `MenuComplete` 在候选数超过 `CompletionQueryItems`
时的确认提示）——说明处理器绑好了、只是**接受分支没命中**，落回了菜单。

根因：`PredictionSource=HistoryAndPlugin` 时内联视图里**同时**有插件建议和
**历史**建议，而历史建议是同步立刻出现的；本模块只认自家快照，输入一多、或按键早于
worker 出结果，快照就对不上 → 判为"无建议" → 弹菜单。**同一份事实存在两个来源，
必然打架。**

**第二版（`55f99c3`，现行）改为委托引擎**：

- 接受动作调 `[Microsoft.PowerShell.PSConsoleReadLine]::AcceptSuggestion($key, $null)`
  —— 2.4.5 `Prediction.cs` 里它是"内联视图有活动建议则插入其后缀"，**一次覆盖插件
  建议与历史建议**，并自带引擎语义（`_current = _buffer.Length` 后插入）与状态同步
  （`OnSuggestionAccepted`），不会留下陈旧建议；
- 无建议时同一份源码显示它是**空操作**（`HasActiveSuggestion` 为假直接返回、不移动
  光标），故用 `Get-CmdPilotBuffer` 比对**调用前后的行内容**：变了=已接受，没变=
  本来就没有建议 → 才退回 `MenuComplete`；缓冲区读不到（`cursor < 0`）时直接走菜单，
  与改动前行为一致、不做猜测；
- 随之**删除 `PeekSuggestion`** —— 与引擎重复的第二份真相正是本 bug 的来源。
- 列表菜单仍留在 PSReadLine 默认键位 **Ctrl+@**，能力未丢。

**→ 不需要绑定**：PSReadLine 默认 `RightArrow` → `ForwardChar`，而 `ForwardChar`
的语义正是"光标在行尾时接受整条建议"——PSReadLine 自带
`SamplePSReadLineProfile.ps1` 原文："`ForwardChar` accepts the entire suggestion
text when the cursor is at the end of the line."，与 Tab 的判据完全一致，
故 README 承诺的"Tab 或 → 接受"无需额外实现。实测键位：
`Tab → CmdPilotAcceptOrMenu`、`RightArrow → ForwardChar`、`Ctrl+@ → MenuComplete`。
注意 PSReadLine **不认 `Ctrl+Space` 这个键名**（`Get-PSReadLineKeyHandler -Key Ctrl+Space`
报 `Unrecognized key 'Space'`）；物理按 Ctrl+Space 产生的就是 `Ctrl+@`（NUL，0x00），
实际按下去即可弹出菜单。

### 3. 宿主 `$Error` 红字——三处"异常当控制流"（提交 `1e8f598`）

异常即使被 `catch`，也会作为一条记录留在**宿主**的 `$Error` 里（用户敲 `$Error`
看到红字）。实测关键事实：**模块的 `$Error` 与宿主的 `$Error` 是两份列表**
（`[object]::ReferenceEquals($Error, $global:Error)` = `False`），在模块内无论怎么
`Clear`/摘除都清不掉宿主那份——唯一正确的做法是**不抛**。三处来源与改法：

| 位置 | 原做法 | 现做法 |
|---|---|---|
| 引擎 API 探测 | 类型字面量，5.1/7.0–7.3 必抛"无法找到类型" | 扫 `AppDomain.CurrentDomain.GetAssemblies()`，`$asm.GetType($name, $false)` 不抛（跳过 `IsDynamic`）；结论与类型字面量一致：7.6.6 命中 SMA、5.1 未命中 |
| 启用预测 | 直接 `Set-PSReadLineOption -PredictionSource`，由 PSReadLine 抛 `ArgumentException` | 先 `Test-CmdPilotInlinePredictionSupported` 按 PSReadLine **自己的判据**提前返回 |
| 注册/注销 | 直接 `UnregisterSubsystem`，对未注册 Id 抛 | 先 `Test-CmdPilotPredictorRegistered` 查 Id（`GetSubsystemInfo` 只查询、不抛） |

其中第二条的判据来自 PSReadLine 源码（2.2.5 / `PlatformWindows.OneTimeInit`）：
`_enableVtOutput = !Console.IsOutputRedirected && SetConsoleOutputVirtualTerminalProcessing()`，
`_console` 非 VT 即为 `LegacyWin32Console`，而 `Options.cs` 的 `predictionSource` 分支
正是对它抛异常。PowerShell 侧可观察的等价量：`[Console]::IsOutputRedirected` 与
`$Host.UI.SupportsVirtualTerminal`（后者取不到时不拦，宁可真试一次）。

**顺带修掉的潜在 bug**：`Enable-CmdPilotPredictionOptions` 原读写 `PredictionView`，
但 2.4.5 反射实证属性名是 **`PredictionViewStyle`**、参数名 `-PredictionViewStyle`
且**无别名**——即该段从未执行（属性查空），一旦执行就是 `ParameterBindingException`。

### 4. 命令重复上报（提交 `a7f4c60`）

`prompt` 钩子是为 `none` 降级路径补"命令已执行"事件用的（新 API 有
`OnCommandLineExecuted`）。在 `new` 分支再挂一层会让每条命令被上报两次
（`usage_stats` 计数翻倍），并给每次 prompt 渲染加一次 `Get-History` 的固定开销。

### 5. 验证结论

| 场景 | 结果 |
|---|---|
| 真实控制台 pwsh 7.6.6 | `Console.IsOutputRedirected=False`（确系真控制台）、`ApiKind=new`、`PredictionSource=HistoryAndPlugin`、`PredictionViewStyle=InlineView`、`Tab 绑定=CmdPilotAcceptOrMenu`、`AcceptSuggestion` 方法存在且 `$psr::AcceptSuggestion($null,$null)` **不抛绑定异常**、`Get-CmdPilotBuffer` 可用（`cursor=0`）；导入 / 二次导入 / `Disable`×2 后 `global Error count` 均为 **0** |
| 输出被重定向的 pwsh 7 | 6 条建议、inline 为 `'atus'`；`Tab=CmdPilotAcceptOrMenu`、`PeekSuggestion` 已移除、`Get-CmdPilotBuffer` 可用、`AcceptSuggestion` 调用不抛，`Error count=0`（修复前为 2） |
| PS 5.1 | `ApiKind=none`、`TabFallback=True`、`PromptWrapped=True`、`Tab→CmdPilotAITab`，`Error count=0`（修复前为 1） |
| 模块完整性 | `CmdPilot.psm1`/`PredictorNew.ps1` 的 UTF-8 BOM 保留；仓库、PS7 安装目录、PS5.1 安装目录三方 SHA256 一致 |

**未独立验证**：Tab **按键**路径的端到端效果（向用户控制台注入按键会抢焦点，无法
无干扰完成）。已验证处理函数绑定、`AcceptSuggestion` 的调用绑定与存在性、缓冲区
读写与变化判定所依赖的 `Get-CmdPilotBuffer`，以及 PS 5.1 的降级路径；接受动作本身
由引擎执行，仍需在真实会话中按一次 Tab 确认。

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
