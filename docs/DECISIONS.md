# 决策与验证总结

项目：CmdPilot（Windows 命令行智能补全助手）
版本：0.1.0 ｜ 日期：2026-09-15 ｜ 仓库：https://github.com/qiutuan/CmdPilot

## 一、关键决策及理由

| # | 决策 | 理由 | 记录 |
|---|---|---|---|
| 1 | **核心语言选 Go（1.22+，modernc.org/sqlite 纯 Go）** | 免 CGO 交叉编译到 Windows 单二进制、goroutine 天然适配异步 AI、单测/覆盖率工具链成熟；CLI/守护进程/插件 companion 一体 | 全项目 |
| 2 | **终端接入走官方 API：PSReadLine Predictor + Clink**，不做自制控制台钩子 | 官方内联/列表视图零逆向成本、稳定；Clink 为 CMD 事实标准 | ADR-001 |
| 3 | **PowerShell 仅用引擎级 Subsystem API**：PS 7.4+/PSReadLine 2.3.4+ 注册 ICommandPredictor；PS 5.1/7.0–7.3 无任何插件预测 API，退化为 Tab 补全 | 版本矩阵 + 2.2.5 源码/二进制实证：2.2.x 无 ICommandPredictor/RegisterPredictor，且 -PredictionSource Plugin 在 .NET Framework 上抛异常 | 适配层 |
| 4 | **core 零终端依赖 + companion 进程桥**：终端↔守护进程经 JSON/行协议 | 单守护进程共享补全/统计/AI，适配层薄、可独立测试 | 架构文档 |
| 5 | **/complete 恒同步返回本地结果，AI 异步 debounce（300ms）+ 1 in-flight + 5min 缓存** | 本地 P95≤10ms 硬指标不被网络污染；AI 失败静默降级 | ADR-002 |
| 6 | **降级链**：AI 超时(5s)/500/断网/无效 key/畸形响应 → 本地 → 历史前缀 | AI 永不成为单点，终端永不卡输入 | ADR-002 |
| 7 | **排序公式**：`log(1+count) × 0.5^(age/30d) × 上下文(×1.5)`，收藏保底 2.0 | 对数抑制高频边际、半衰期衰减贴合节奏、上下文体现"越用越懂你" | ADR-003 |
| 8 | **key 用 Windows DPAPI 加密存储**（非 Windows dev: 前缀占位）；`CMDPILOT_AI_API_KEY` 仅内存 | Windows 内置免依赖、用户级加密；环境变量不落盘 | ADR-004 |
| 9 | **内置知识库用 gzip JSON（embed）**，用户覆盖走独立表 | 体积 23.8KB、启动 <1ms；内置与用户数据天然隔离 | ADR-006 |
| 10 | **上下文状态机**：命令名/子命令/参数/路径分状态补全 + git 仓库探测 + 链式推荐 | 显式化"该补什么"，评测 Top-5 100% | ADR-005 |
| 11 | **配置损坏自动备份+回退默认**，绝不启动失败 | 用户机器上配置损坏不应让终端功能消失 | config 包 |
| 12 | **SQLite WAL + synchronous=NORMAL + busy_timeout + user_version 迁移** | kill -9 断电式崩溃可自动恢复、无半写入（稳定性测试验证） | db 包 |
| 13 | **评测口径采用官方 go test 覆盖率**（此前自研解析与 go tool cover 不一致，已修正） | 报告必须与权威工具一致，可复现 | tools/eval |
| 15 | **DPAPI 实现直接使用 x/sys/windows.DataBlob**；daemon stop 兜底 kill 用 os.Process.Kill（跨平台） | Windows 交叉编译实测发现自定义 dataBlob 类型不兼容、syscall.Kill 在 Windows 不存在，均为真实 bug，已修复并加交叉编译验证 | secrets_windows.go / cli.go |
| 14 | **token 无 workflow 权限时 CI 以示例文件入库** | GitHub 拒绝 PAT 更新 .github/workflows；获得相应权限后复制即启用 | docs/windows-ci.example.yml |
| 16 | **Tab = "有内联建议则接受、无则退回原生菜单"，接受动作委托引擎 `AcceptSuggestion`** | 只弹菜单不落字对灰色建议等于无效，与 README 承诺不符。接受必须走引擎 API：它一次覆盖插件建议与**历史**建议（HistoryAndPlugin 下历史建议同步立刻出现），并自带插入语义与状态同步；本模块自己读快照插入等于维护第二份真相，实测必然打架（"看得见幽灵文本但 Tab 无效"，按 Tab 得到 MenuComplete 的 `Display all N possibilities?`） | 提交 `bb40821` → `55f99c3` |
| 17 | **预测器 worker 每代输入只取一次结果**（`$lastGen` 记账，取前先记账） | 写完快照不改变 `gen`/`pending`，"输入未变"判断会对同一输入无限重取——实测空闲 6 秒起 18 个 companion 进程；取前记账可避免失败重试风暴。**注**：此处曾写作"是输入延迟的根源"，实测（见 19）它只是次要项，每键的主成本是引擎 20ms 超时 | 提交 `97fd6ec` |
| 18 | **模块内不抛异常**（探测/启用/注销一律走正常分支） | 异常的记录进的是**宿主** `$Error`，与模块 `$Error` 是两份列表（`ReferenceEquals` 实测 False），模块内清不掉；用户会看到红字 | 提交 `1e8f598` |
| 19 | **预测器回调（`GetSuggestion`）内只允许编译代码**：本体为 `PredictorCore.cs`（`Add-Type` 编译的 `ICommandPredictor`），取建议的慢路径（debounce＋companion 进程＋JSON 解析）留在独立 runspace 的 worker | PSReadLine 渲染内联建议是**同步**的（`Render.cs` → `_predictionTask.Result`），而引擎把回调丢线程池只等 20ms、超时即丢弃结果。PowerShell 类方法每次必踩满：实测 31ms/键 = 20ms 超时 + .NET 定时器 15.6ms 粒度，且 `predictors=0`（结果全被丢弃，等于从未真正显示过）；编译实现 0.42ms、`predictors=1`。真机按键回声 44.5→15.6ms（无模块基线 15.5ms） | 提交 `5a333ed` `da23e45` |
| 20 | **worker 的 cwd 取自控制台当前位置**（`PredictionClient.CurrentLocation`，随输入**同代**记在 `Register` 里），不用 worker 自己 runspace 的 `Get-Location` | 后者是**进程**工作目录：实测同一会话 console cwd=`%TEMP%\cmdpilot-cwdprobe-xxxx`、进程 cwd=仓库根。cwd 是路径类建议与上下文（`splitPath`/`IsGitRepo`/`TopNByDir`）的入参，用错则 cd 之后路径补全按启动目录算 | 提交 `0cd8c95` |
| 21 | **CMD 适配层以真机实测 API 为准**：`clink.generator(priority)`+`obj:generate`、`line_state:getline()/:getcursor()`、`rl.gethistory*`、`match_builder:setnosort(true)`；补全词由**裁剪后的** `line_state` 自行推算，不依赖 `getendword` | 旧插件调用的 `clink.script_dir`/`register_generator`/`gethistory`/`getline`/`getpoint`/`getcwd` 在 1.9.33.a4bf0e 上**全部不存在**，脚本加载即报错——这才是"cmd 窗口没生效"的直接原因。裁剪是 Clink 的固有设计：它随后用**完整末尾词**过滤我们返回的匹配（实测 `git co`+Tab→`git config`，而裁剪输入 `git c` 的 rank1 是 `git clone`），所以插件必须按引擎排名交给它过滤，而不是自己去猜末尾词。默认字母表重排序会冲掉引擎排名，故 `setnosort(true)` | 提交 `fed9f52` |
| 22 | **CMD 插件安装到 `clink info` 报告的 `state` 目录当层**，复制后校验落地（存在性+SHA256） | Clink 只加载脚本目录**当层**的 `*.lua`，任意子目录不加载（`completions\` 是唯一例外且按需加载）——旧安装器写的 `%LOCALAPPDATA%\clink\CmdPilot\` 永远不会被加载，而"复制成功"当时只是打印出来的、没检查结果 | 提交 `787617e` |

## 二、测试执行摘要（全部可一键复跑：`go run ./tools/eval all`）

| 项 | 结果 | 报告 |
|---|---|---|
| 单元测试 | 16 个包全部通过 | go test -count=1 ./... |
| 覆盖率 | 核心层平均 **85.1%**（≥85%）；AI 响应解析 **100%**；match 96.1% / rank 96.2% / sanitize 100% | docs/reports/coverage.md |
| 引擎质量 | 231 条真实场景 **Top-5 100%**（Top-1 97.4%） | docs/reports/engine-eval.md |
| 降级矩阵 | 超时/HTTP 500/断网/无效 key/畸形响应 → 全部返回本地 Top、ai_used=false、无错误冒泡 | docs/reports/degradation.md |
| 脱敏 | 7 种敏感样本（sk-/AKIA/ghp_/Bearer/password/token/api_key，FAKE_ 格式）注入历史 → mock 服务器捕获请求体 **0 泄漏** | docs/reports/sanitize-capture.md |
| 稳定性 | 并发上报中 kill -9 → integrity ok、数据可读、守护进程重启、补全/写入正常 | docs/reports/stability.md |
| E2E | **12/12**：启动→建议→上报→统计→推荐→收藏→配置→导出导入→AI test→自检→重启持久→清空保留→无效 AI 降级 | docs/reports/e2e.md |
| 性能 | 本地补全 P95 达 ≤10ms 硬指标；万条历史延迟；RSS ≤30MB；空闲 CPU≈0%（事件驱动）；AI 慢 1.2s 时首返 ≤500ms 非阻塞 | docs/reports/performance.md |
| 安装包体积 | Windows amd64 交叉编译：cmdpilot.exe 11.4MB + cmdpilot-clink.exe 10.3MB，gzip 安装包合计 **9.3MB ≤ 20MB** 硬指标达标 | 实测（`GOOS=windows go build -ldflags "-s -w"` + gzip） |
| CMD 端到端（真实控制台按键注入） | 8/8：`git commi`→`git commit`、`git statu`→`git status`、`git s`→`git status`（引擎 rank1 非字母序第一）+ 5 条循环、`gi`→`git status`、`git ch`→`git checkout`、`git co`→`git config`（弹窗 2 条，clone/clean 被正确筛掉）、`cd do`→`cd docs\`、启动打印启用行 | docs/install-report-windows.md「补记四次」 |

## 三、本地验证边界（诚实声明）

以下项需要 Windows 真机/CI runner。带"已实测"的是 2026-09-16 在 Windows 11 真机上
完成的验证（方法与原始数据见 docs/install-report-windows.md），其余仍待真机/CI：

1. **PS 5.1 与 PS 7.0–7.3 路径**：Windows 真机 pwsh 7.6.6 已实机验证 Subsystem
   API 路径（注册→GetSuggestion 幽灵文本→注销零残留，见 install 报告补记）。
   PS 5.1 已实测无任何插件预测 API（2.2.5 源码/二进制实证），退化为 Tab；
   7.0–7.3 同 5.1（无引擎级 API），无需旧路径实现。编译核心（`PredictorCore.cs`）
   只在 7.4+ 路径上被 `Add-Type`（探测为 `none` 时 `PredictorNew.ps1` 根本不
   dot-source），故 PS 5.1 既不会编译它、也不会因编译失败受影响。
2. **Clink 真机交互**：已在 Windows 真机用**真实控制台按键注入**实测
   （`FreeConsole`→`AttachConsole`→`WriteConsoleInputW`/`ReadConsoleOutputCharacterW`）：
   `git commi`→`git commit`、`git s`→`git status`（引擎 rank1 而非字母序第一）、
   `git co`→`git config`、`gi`→`git status`、`cd do`→`cd docs\`，
   启动打印启用行、`clink.log` 记 `Loaded 1 Lua scripts`。CLI 侧只剩用户在自己
   窗口里按一次 Tab 的确认（见 install 报告"补记四次"）。
3. **DPAPI 加密**：Windows 专属路径（secrets_windows.go），非 Windows 为
   dev 占位；单测覆盖占位路径。
4. **安装/卸载脚本**：**已实测**（Windows 11 + PS 5.1.26100：构建→双模块目录安装→
   Clink 插件安装+落地校验→幂等写 `$PROFILE`，exit 0）。仓库与两个安装目录的
   模块文件内容一致（仅 `bin\*.exe` 为安装产物、仓库不跟踪）。
5. **DPAPI 真机加解密**：交叉编译通过、类型与 x/sys API 对齐；真实
   CryptProtectData 往返依赖 Windows 实测（CI 已含构建，可加冒烟）。
5. **演示 GIF**：Windows 终端画面无法在 Linux 录制，交付演示脚本
   （见 docs/demo/README.md），供在真机一键录制。

## 四、修复记录（关键）

- 引擎评测 32.9%→100%：尾随空格归一化比较；补 20 条 CMD 系统工具；
  external 工具 shell 标记统一（cmd/ps 通用）修复 git rm/git mv 过滤。
- PS 7.4 冒烟排障：ISubsystem Id 为 Guid；UnregisterSubsystem 以 Guid 为参；
  raw 线程不能跑脚本块（改独立 runspace + 共享 hashtable）；StrictMode
  属性探测；SuggestionPackage 不可返回 null（用 \u200B 空条目）。
- companion 行协议：stuffing 分隔符/转义必须扫描式区分解码（unstuffSplit）。
- E2E 修复：favorite add 需 --name/--command；配置变更须重启守护进程；
  api_key_encrypted 必须为合法 Protect 值。
- 覆盖率口径：改为官方 go test -json 解析（自研解析与 go tool cover 不符）。
- GitHub push protection：测试 fixture 一律 FAKE_ 前缀（真实 secret 格式
  会被 GH013 拒绝，历史已 filter-branch 重写）。
- 幽灵文本可用后的两个回归（2026-09-16 实机）：Tab 固定绑 MenuComplete 导致
  "有建议也不落字"；worker 无"本代已取过"标记导致空转重取（空闲 6 秒 18 次
  companion 进程 → 加记账后 1 次），表现为窗口卡顿/输入延迟。
- 宿主 `$Error` 红字三来源（引擎探测／`Set-PSReadLineOption` 在非 VT 宿主／
  `UnregisterSubsystem` 未注册 Id），全部改为正常分支而非 `catch` 吞异常；
  判据取自 PSReadLine 2.2.5 源码（`_console` 为 `LegacyWin32Console` 即抛）。
- `Enable-CmdPilotPredictionOptions` 属性/参数名笔误：真名 `PredictionViewStyle`
  （2.4.5 反射实证，无 `PredictionView` 亦无别名），旧写法使该段恒不执行。
- Tab 接受建议不能自建实现（第一版读自家快照后 `Insert` 失败）：内联视图里的建议
  有两个来源，`PredictionSource=HistoryAndPlugin` 下历史建议同步立刻出现，自建快照
  对不上就退回菜单；改调引擎 `AcceptSuggestion`（无建议时为空操作，用缓冲区前后
  比对区分）后一次覆盖两者。
- **"输入长命令后窗口变卡、删掉也照样卡"（2026-09-16 实机）**：根因不在命令内容，
  而在每次按键都要走一遍的预测器回调。引擎等 20ms、PowerShell 类方法必超时——
  实测 31ms/键（= 20ms 超时 + 15.6ms 定时器粒度）且 `predictors=0`，即每敲/每删
  一键白等一次超时，且本模块的建议从未真正送达。改为编译核心后按键回声
  44.5–48.4ms → 15.6ms（无模块基线 15.5ms），无头引擎 n=97 avg 0.121ms
  `over20=0`，端到端建议 1→4 条真正进入引擎。见决策 19。
- `PowerShellAsyncResult` 没有 `WaitOne`（`BeginInvoke` 的返回值），旧实现直接调会抛、
  被 `catch` 吃掉后仍在宿主 `$Error` 留一条红字；改等 `IAsyncResult.AsyncWaitHandle`。
- 模块重载会让旧实例的 worker 变僵尸（`Import-Module -Force` 后新旧两个 worker 读同
  一份状态、每输入起两次 companion）：给核心加 `private static _current` 并在
  `WorkerAlive()` 里 `ReferenceEquals(_current, this)`，旧实例自行退出。
- worker 的 cwd 曾取自己 runspace 的 `Get-Location`（= 进程工作目录，cd 之后就是错的），
  改为随输入同代记录控制台位置。见决策 20。
- **CMD 侧"装了但没反应"是三个独立缺陷叠加，且三者都静默**（2026-09-16 真机）：
  ① 安装器把插件复制到 `%LOCALAPPDATA%\clink\CmdPilot\`——Clink 不递归加载子目录，
  该文件永远不被读取；② Lua 调用的 `clink.script_dir`/`register_generator`/`gethistory`/
  `getline`/`getpoint`/`getcwd` 在 1.9.33.a4bf0e 上**全部不存在**，脚本加载即报错；
  ③ 行协议侧 `io.open("w")` 的 CRLF 让魔数校验失败、`cmd /c` 吃掉首 token 引号、
  history 多值字段被二次 stuff 并成一条。分别修于 `787617e`/`fed9f52`/`74d1f17`。
- `clink info --profile x` 写法无效：`--profile` 是**全局选项**（须在动词之前），
  旧安装器因此把输出第一行 `version : 1.9.33.a4bf0e` 当成了脚本目录名；
  `clink` 也不在本机 PATH 上，需从 cmd 的 AutoRun 注册表项或常见目录定位。
- `--profile ~\clink` 里的 `~` **不会被 Clink 展开**（`clink --profile '~\clink' info`
  → `state : ~\clink`），于是每个 cmd 启动目录下都会新建一个 `~\clink`；真实会话
  实际使用 `%LOCALAPPDATA%\clink`。安装器改为检测并提示改成绝对路径。
- 机器级 `GOROOT` 与用户安装的 Go 版本不一致会让所有构建报
  `compile: version ... does not match go tool version ...`；安装器改为用所选
  `go.exe` 自身的根覆盖 `GOROOT`。
- 提示文字里的 `"\$PROFILE"` 不是转义（反斜杠是字面字符、`$PROFILE` 照常展开），
  三处提示实际打印 `\C:\Users\...`；改用反引号。

## 五、已知限制

- Clink 无自动建议（幽灵文本）API：CMD 侧以 Tab 菜单补全为最佳替代。
- CMD 路径补全位置 Clink 原生补全同时生效：`cd do`+Tab 得 `cd docs\` 与原生项的
  **公共前缀**同文本，实测无法区分归属（详见 install 报告"补记四次"的口径说明）。
- 弹窗列表里 Tab 循环顺序按 Clink 自己的显示排序，只有**首次** Tab 插入的是引擎
  rank1；引擎排名的完整呈现依赖 Clink 列表视图的排序策略。
- AI 补全仅支持 OpenAI 兼容远端协议（按需求，禁本地推理）。
- 万条历史性能在本地压测通过；超大规模（>10 万条）未专项压测。
- CI 工作流因当前 PAT 权限以示例入库，需 workflow 权限 token 启用。
- 统计/收藏跨机器迁移需手动导出导入（favorites.json），统计本身不迁移。
