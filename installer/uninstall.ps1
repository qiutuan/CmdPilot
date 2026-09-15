<#
.SYNOPSIS
  CmdPilot 完全卸载脚本。移除模块、Clink 插件、PROFILE 加载行、守护进程；
  用户数据（收藏/统计/配置/key）默认保留，-RemoveData 时一并清除。
.EXAMPLE
  .\uninstall.ps1
.EXAMPLE
  .\uninstall.ps1 -RemoveData
#>
[CmdletBinding()]
param([switch]$RemoveData)

$ErrorActionPreference = "Stop"
function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-OK($msg) { Write-Host "    $msg" -ForegroundColor Green }

Write-Step "CmdPilot 卸载开始"

# 1. 停守护进程
if (Get-Command cmdpilot -ErrorAction SilentlyContinue) {
    cmdpilot daemon stop | Out-Null
    Write-OK "守护进程已停止"
}

# 2. 移除 PowerShell 模块
foreach ($dir in @("$HOME\Documents\PowerShell\Modules", "$HOME\Documents\WindowsPowerShell\Modules")) {
    $target = Join-Path $dir "CmdPilot"
    if (Test-Path $target) {
        Remove-Item $target -Recurse -Force
        Write-OK "已删除模块 $target"
    }
}

# 3. 移除 Clink 插件
$ClinkDirs = @(
    (Join-Path $env:LOCALAPPDATA "clink\CmdPilot"),
    (Join-Path $env:LOCALAPPDATA "clink")
)
foreach ($dir in $ClinkDirs) {
    foreach ($f in @("cmdpilot.lua", "cmdpilot-clink.exe", "cmdpilot.exe")) {
        $p = Join-Path $dir $f
        if (Test-Path $p) { Remove-Item $p -Force; Write-OK "已删除 $p" }
    }
    if ($dir -like "*CmdPilot" -and (Test-Path $dir) -and -not (Get-ChildItem $dir)) {
        Remove-Item $dir -Force
    }
}

# 4. 移除 PROFILE 加载行
$profilePath = $PROFILE.CurrentUserAllHosts
if (Test-Path $profilePath) {
    $lines = Get-Content $profilePath | Where-Object { $_ -notmatch "CmdPilot" }
    Set-Content $profilePath $lines
    Write-OK "已清理 \$PROFILE 中的 CmdPilot 加载行"
}

# 5. 数据目录（可选）
if ($RemoveData) {
    foreach ($candidate in @(
        (Join-Path $env:LOCALAPPDATA "CmdPilot"),
        "$HOME\.local\share\cmdpilot"
    )) {
        if (Test-Path $candidate) {
            Remove-Item $candidate -Recurse -Force
            Write-OK "已删除数据目录 $candidate"
        }
    }
} else {
    Write-Host "    已保留用户数据（收藏/统计/配置），如需删除请加 -RemoveData"
}

Write-Step "卸载完成，无残留。"
