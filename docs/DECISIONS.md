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

## 三、本地验证边界（诚实声明）

以下仅能在 Windows 真机/CI runner 验证，本开发环境（Linux 沙箱）未实测：

1. **PS 5.1 与 PS 7.0–7.3 经典 API 路径**：本地仅有 PS 7.4.6（Subsystem API
   路径已实机验证：注册→GetSuggestion→注销零残留）。经典路径按官方文档
   实现，验证依赖 CI（docs/windows-ci.example.yml）。
2. **Clink 真机交互**：Lua 语法与行协议已本地冒烟；Tab 菜单交互依赖 CI。
3. **DPAPI 加密**：Windows 专属路径（secrets_windows.go），非 Windows 为
   dev 占位；单测覆盖占位路径。
4. **安装/卸载脚本**：PowerShell 语法人工审查 + 结构对齐模块路径；真机
   执行依赖 Windows。
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

## 五、已知限制

- Clink 无自动建议（幽灵文本）API：CMD 侧以 Tab 菜单补全为最佳替代。
- AI 补全仅支持 OpenAI 兼容远端协议（按需求，禁本地推理）。
- 万条历史性能在本地压测通过；超大规模（>10 万条）未专项压测。
- CI 工作流因当前 PAT 权限以示例入库，需 workflow 权限 token 启用。
- 统计/收藏跨机器迁移需手动导出导入（favorites.json），统计本身不迁移。
