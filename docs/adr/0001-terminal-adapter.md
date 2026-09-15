# ADR-001：终端接入方案选型

- 状态：已接受
- 日期：2026-09-15
- 决策者：主工程师（自主决策，按最小惊讶原则）

## 背景
Windows 原生命令行没有 fish/Copilot 式自动补全；可选的接入方式有：
PSReadLine Predictor API、Clink 插件 API、自制控制台钩子（挂接 ReadConsoleW）。

## 决策
**采用官方 API 双适配**：PowerShell → PSReadLine Predictor；CMD → Clink
Lua 插件。**明确不做**自制控制台钩子。

## 理由
1. PSReadLine ≥2.2 提供官方 ICommandPredictor：引擎级 Subsystem API
   （PS 7.4+/PSReadLine 2.3.4+）或经典 API（PS 5.1/7.0–7.3），原生内联
   幽灵文本 + 列表视图，无需逆向。
2. Clink 是 CMD 事实标准的补全框架，register_generator 注册补全器，
   `clink inject` 即可生效，天然支持 git 等外部工具补全。
3. 自制 ReadConsoleW 钩子：x86/x64 兼容矩阵、各终端宿主差异、稳定性和
   安全风险高，违背"轻量、可靠"定位。

## 后果
- Clink 无自动建议（幽灵文本）API：以 Tab 菜单补全为最佳替代
  （Enter/Tab 接受、Esc 取消），已在 README/架构文档中如实说明。
- 两适配层共用 companion 进程与 core，避免逻辑重复。
