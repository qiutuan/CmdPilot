<#
.SYNOPSIS
  CmdPilot 60 秒演示（PowerShell）：幽灵文本 / Clink Tab / AI / 收藏 / 高频 / 降级。
.DESCRIPTION
  在 PowerShell 会话中逐步展示各场景；建议配合录屏工具录制 60 秒。
  演示为"引导式"：脚本打印每步操作指引并执行可自动化的部分，
  幽灵文本的接受动作由人工按键完成（Tab / Enter / Esc）。
#>
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)

Write-Host "===== CmdPilot 60s 演示 =====" -ForegroundColor Cyan
Write-Host ""

# --- 0. 准备：mock AI 服务器（不耗真实额度）---
Write-Host "[准备] 启动本地 mock AI 服务器..." -ForegroundColor Yellow
$mockDir = Join-Path $root "tools\demo"
if (Test-Path (Join-Path $mockDir "mock-ai.exe")) {
    Start-Process (Join-Path $mockDir "mock-ai.exe") -WindowStyle Hidden
    Start-Sleep -Milliseconds 500
}

# --- 1. 幽灵文本补全（PS）---
Write-Host ""
Write-Host "[1/6] PowerShell 幽灵文本补全" -ForegroundColor Green
Write-Host "  手动操作：输入 'git st' -> 出现灰色建议 'git stash' -> 按 Tab 接受"
Write-Host "  （若未自动出现建议，先执行：cmdpilot config set engine hybrid）"

# --- 2. CMD/Clink Tab 补全 ---
Write-Host ""
Write-Host "[2/6] CMD + Clink Tab 菜单补全" -ForegroundColor Green
Write-Host "  手动操作：打开 CMD，输入 'git c' 后按 Tab，候选列表出现 git checkout/commit/...，Enter 接受"

# --- 3. AI 补全 ---
Write-Host ""
Write-Host "[3/6] AI 补全（mock server）" -ForegroundColor Green
cmdpilot config set ai.base_url "http://127.0.0.1:18888/v1"
cmdpilot config set ai.model "mock-gpt"
cmdpilot config set ai.api_key "demo-key"
cmdpilot ai test
Write-Host "  手动操作：输入 'docker ps -' 后停顿约 1s，观察 AI 后缀建议（灰色）"

# --- 4. 收藏命令 ---
Write-Host ""
Write-Host "[4/6] 收藏命令参与补全" -ForegroundColor Green
cmdpilot favorite add --name "demo-deploy" --command "git push origin main && npm run build" --tags demo
Write-Host "  手动操作：输入 'demo-' 观察收藏建议，Tab 接受"

# --- 5. 高频推荐 ---
Write-Host ""
Write-Host "[5/6] 高频推荐（越用越懂你）" -ForegroundColor Green
cmdpilot complete --input "" --shell ps --json | Select-Object -First 1
Write-Host "  手动操作：空输入停顿 1s，观察推荐 Top（优先本目录高频）"

# --- 6. 断网降级 ---
Write-Host ""
Write-Host "[6/6] 断网降级（AI 永不成为单点）" -ForegroundColor Green
Write-Host "  手动操作：停掉 mock server（任务管理器结束 mock-ai）后继续输入，"
Write-Host "  建议仍来自本地引擎，终端无任何卡顿。"
cmdpilot config set engine local
cmdpilot complete --input "git st" --shell cmd

Write-Host ""
Write-Host "===== 演示结束（约 60 秒）=====" -ForegroundColor Cyan
