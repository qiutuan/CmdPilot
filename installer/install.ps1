<#
.SYNOPSIS
  CmdPilot 一键安装脚本（PowerShell，-Scope CurrentUser，无需管理员）。
.DESCRIPTION
  安装内容：
    1. 构建/下载 Go 二进制（cmdpilot 守护进程 + cmdpilot-clink 伴侣进程）；
    2. 安装 PowerShell 模块（CmdPilot）到 $HOME\Documents\PowerShell\Modules；
    3. 复制 Clink Lua 插件到 %LOCALAPPDATA%\clink\CmdPilot 与检测到的 clink 脚本目录；
    4. 写入 $PROFILE 自动加载行（幂等）；
    5. 检测 Clink：未安装时给出 winget install clink 引导（不报错退出）。
  支持 -SkipBuild 使用预编译产物、-DataDir 指定数据目录。
.EXAMPLE
  .\install.ps1
.EXAMPLE
  .\install.ps1 -SkipBuild
#>
[CmdletBinding()]
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-OK($msg)   { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn2($msg) { Write-Host "    [!] $msg" -ForegroundColor Yellow }

Write-Step "CmdPilot 安装开始 (PowerShell $($PSVersionTable.PSVersion.ToString()))"

# ---------- 1. 二进制 ----------
$BinDir = Join-Path $Root "bin"
if (-not $SkipBuild) {
    Write-Step "构建 Go 二进制（需要 Go ≥1.22）"
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "未找到 go 命令。请安装 Go 或使用 -SkipBuild 配合预编译二进制。"
    }
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Push-Location $Root
    try {
        $env:CGO_ENABLED = "0"
        go build -trimpath -ldflags "-s -w" -o (Join-Path $BinDir "cmdpilot.exe") ./cmd/cmdpilot
        if ($LASTEXITCODE -ne 0) { throw "cmdpilot.exe 构建失败" }
        go build -trimpath -ldflags "-s -w" -o (Join-Path $BinDir "cmdpilot-clink.exe") ./cmd/cmdpilot-clink
        if ($LASTEXITCODE -ne 0) { throw "cmdpilot-clink.exe 构建失败" }
    } finally { Pop-Location }
    Write-OK "构建完成"
} else {
    if (-not (Test-Path (Join-Path $BinDir "cmdpilot.exe"))) {
        Write-Warn2 "-SkipBuild 但未找到 $BinDir\cmdpilot.exe，跳过二进制步骤"
    }
}
$CompanionExe = Join-Path $BinDir "cmdpilot-clink.exe"

# ---------- 2. PowerShell 模块 ----------
Write-Step "安装 PowerShell 模块 CmdPilot"
$PSModules = @(
    "$HOME\Documents\PowerShell\Modules",
    "$HOME\Documents\WindowsPowerShell\Modules"
)
$ModSrc = Join-Path $Root "adapters\powershell\CmdPilot"
$Installed = $null
foreach ($dir in $PSModules) {
    $target = Join-Path $dir "CmdPilot"
    New-Item -ItemType Directory -Force -Path $target | Out-Null
    Copy-Item -Path (Join-Path $ModSrc "*") -Destination $target -Recurse -Force
    # 复制 Windows 二进制到模块 bin（PowerShell 插件经 companion 调用）
    if (Test-Path $CompanionExe) {
        New-Item -ItemType Directory -Force -Path (Join-Path $target "bin") | Out-Null
        Copy-Item $CompanionExe (Join-Path $target "bin") -Force
        Copy-Item (Join-Path $BinDir "cmdpilot.exe") (Join-Path $target "bin") -Force
    }
    $Installed = $target
    Write-OK "已安装到 $target"
}
if (-not $Installed) { throw "无法确定 PowerShell 模块目录" }

# ---------- 3. Clink 插件 ----------
Write-Step "安装 Clink Lua 插件"
$LuaSrc = Join-Path $Root "adapters\clink\cmdpilot.lua"
$ClinkScriptDir = $null
if (Get-Command clink -ErrorAction SilentlyContinue) {
    $ClinkScriptDir = (clink info --profile 2>$null | ForEach-Object { $_ } | Select-Object -First 1)
}
if (-not $ClinkScriptDir) {
    $ClinkScriptDir = Join-Path $env:LOCALAPPDATA "clink"
}
New-Item -ItemType Directory -Force -Path $ClinkScriptDir | Out-Null
Copy-Item $LuaSrc $ClinkScriptDir -Force
Copy-Item $CompanionExe (Join-Path $ClinkScriptDir "cmdpilot-clink.exe") -Force -ErrorAction SilentlyContinue
Write-OK "Clink 插件已复制到 $ClinkScriptDir"

# Clink 未安装？引导但不报错退出
if (-not (Get-Command clink -ErrorAction SilentlyContinue)) {
    Write-Warn2 "未检测到 Clink。CMD 补全需要 Clink，可稍后执行："
    Write-Host "        winget install clink" -ForegroundColor White
    Write-Host "        或从 https://chrisant996.github.io/clink/ 下载官方安装包" -ForegroundColor White
    Write-Host "        （安装 Clink 后 CMD 自动加载 $ClinkScriptDir 下的插件）" -ForegroundColor White
}

# ---------- 4. $PROFILE 自动加载 ----------
Write-Step "写入 \$PROFILE 自动加载"
$profilePath = $PROFILE.CurrentUserAllHosts
New-Item -ItemType Directory -Force -Path (Split-Path $profilePath) | Out-Null
if (-not (Test-Path $profilePath)) { New-Item -ItemType File -Path $profilePath | Out-Null }
$loadLine = 'Import-Module CmdPilot -ErrorAction SilentlyContinue'
$content = Get-Content $profilePath -Raw -ErrorAction SilentlyContinue
if ($content -notmatch [regex]::Escape($loadLine)) {
    Add-Content $profilePath "`n# CmdPilot 自动加载（安装器写入）`n$loadLine"
    Write-OK "已写入 $profilePath"
} else {
    Write-OK "PROFILE 已包含加载行，跳过"
}

# ---------- 5. 数据目录（可选重定向） ----------
if ($DataDir) {
    # 二进制不读 CMDPILOT_DATA_DIR；此处仅为文档性占位。
    # 数据目录实际由 %LOCALAPPDATA%\CmdPilot 决定（或 CMDPILOT_ 环境变量族）。
    Write-OK "提示：数据目录默认在 %LOCALAPPDATA%\CmdPilot（-DataDir 需应用层支持，当前为占位）"
}

Write-Step "安装完成。"
Write-Host ""
Write-Host "下一步："
Write-Host "  1. 打开新 PowerShell 窗口（或 . \$PROFILE）——启用提示一行并开始幽灵文本补全"
Write-Host "  2. 配置 AI（可选）：cmdpilot config set ai.base_url <url>; cmdpilot config set ai.api_key <key>; cmdpilot config set ai.model <model>"
Write-Host "  3. CMD：安装 Clink 后即生效；验证：clink inject"
Write-Host "  4. 卸载：.\uninstall.ps1"
