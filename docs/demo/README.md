# CmdPilot 演示脚本（60 秒 GIF / 录屏）

本目录提供在 Windows 真机上一键录制 60 秒演示的脚本，覆盖需求要求的
6 个场景。开发环境为 Linux，无法直接录制 Windows 终端画面，故交付脚本
而非 GIF 成品。

## 演示内容（对应需求）

| 时间段 | 场景 |
|---|---|
| 0–10s | PowerShell 幽灵文本补全（输入 `git st` → 灰色建议 `git stash` → Tab 接受） |
| 10–20s | CMD/Clink Tab 菜单补全（`git c` → 候选列表 → Enter 接受） |
| 20–35s | AI 补全（本地 mock server 模拟 OpenAI 兼容 API，演示 300ms debounce 与后缀补全） |
| 35–45s | 收藏命令（`cmdpilot favorite add` → 输入前缀出现收藏建议） |
| 45–52s | 高频推荐（执行同一命令多次后，空输入显示 Top 推荐） |
| 52–60s | 断网降级（停掉 AI → 输入前缀仍得到本地建议，终端无卡顿） |

## 用法

```powershell
# 1. 安装（见 README），然后：
powershell -ExecutionPolicy Bypass -File docs\demo\demo.ps1

# 2. 用 Windows 自带 Game Bar (Win+G) 或 OBS 录制屏幕 60 秒
#    也可用 PowerShell 脚本自动截图合成：
#    （可选）Install-Module -Name PSWriteHTML / 或使用 ffmpeg gdigrab：
#    ffmpeg -f gdigrab -framerate 10 -video_size 1280x800 -offset_x 0 -offset_y 0 -t 60 -i desktop demo.gif
```

## 本地（无 Windows）验证替代

以下命令在 Linux 沙箱即可验证核心链路（已随仓库测试跑通）：

```bash
# 幽灵文本等价验证（companion JSON 模式）
go run ./cmd/cmdpilot-clink --request /tmp/req.json --output /tmp/out.json   # JSON 模式

# CLI 补全/推荐/AI mock 验证
go run ./cmd/cmdpilot complete --input "git st" --shell cmd --json
go run ./cmd/cmdpilot recommend --json

# 断网降级验证
go run ./tools/eval degrade   # 5 种故障矩阵全绿
```

## 说明

- `demo.ps1` 中的 AI 演示使用内置 mock server（`tools/demo/mock-ai`），
  不消耗真实 API 额度；真实 API 演示请将 `$env:CMDPILOT_AI_BASE_URL` 指向
  自己的端点。
- 录制建议：关闭终端"快速编辑"、放大字体、显示模式（PS）选 InlineView。
