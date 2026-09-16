<#
.SYNOPSIS
  CmdPilot 一键安装脚本（PowerShell，-Scope CurrentUser，无需管理员）。
.DESCRIPTION
  安装内容：
    1. 构建/下载 Go 二进制（cmdpilot 守护进程 + cmdpilot-clink 伴侣进程）；
    2. 安装 PowerShell 模块（CmdPilot）到 $HOME\Documents\PowerShell\Modules；
    3. 复制 Clink Lua 插件到 clink 报告的脚本目录（默认 %LOCALAPPDATA%\clink，插件必须直接
       放在该目录下——Clink 不递归加载子目录），复制后校验落地结果；
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
    # go 未必在 PATH 上（实测本机 GOROOT=D:\Program Files\Go，但 PATH 里没有 go）
    $GoExe = $null
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if ($goCmd) { $GoExe = $goCmd.Source }
    if (-not $GoExe) {
        $goCands = @()
        if ($env:GOROOT)       { $goCands += (Join-Path $env:GOROOT "bin\go.exe") }
        if ($env:ProgramFiles) { $goCands += (Join-Path $env:ProgramFiles "Go\bin\go.exe") }
        if (${env:ProgramFiles(x86)}) { $goCands += (Join-Path ${env:ProgramFiles(x86)} "Go\bin\go.exe") }
        if ($env:LOCALAPPDATA) { $goCands += (Join-Path $env:LOCALAPPDATA "Programs\Go\bin\go.exe") }
        foreach ($p in $goCands) { if (Test-Path $p) { $GoExe = $p; break } }
    }
    if (-not $GoExe) {
        throw "未找到 go 命令。请安装 Go 或使用 -SkipBuild 配合预编译二进制。"
    }
    # 用所选 go 自身的根目录覆盖 GOROOT：本机存在与实际工具链不匹配的 GOROOT
    # （User=…\Programs\go 是 go1.27.1、Machine=D:\Program Files\Go 是 go1.26.0），
    # 不覆盖会报 "compile: version ... does not match go tool version ..."
    $env:GOROOT = Split-Path (Split-Path $GoExe -Parent) -Parent
    Write-OK "使用 $GoExe（GOROOT=$env:GOROOT）"
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Push-Location $Root
    try {
        $env:CGO_ENABLED = "0"
        & $GoExe build -trimpath -ldflags "-s -w" -o (Join-Path $BinDir "cmdpilot.exe") ./cmd/cmdpilot
        if ($LASTEXITCODE -ne 0) { throw "cmdpilot.exe 构建失败" }
        & $GoExe build -trimpath -ldflags "-s -w" -o (Join-Path $BinDir "cmdpilot-clink.exe") ./cmd/cmdpilot-clink
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

# 定位 clink 可执行文件。clink 一般不在 PATH 上（实测），所以再查 cmd 的 AutoRun
# 和常见安装目录；找不到只说明"未安装"，不影响其它步骤。
function Resolve-ClinkExe {
    foreach ($name in @("clink", "clink_x64")) {
        $c = Get-Command $name -ErrorAction SilentlyContinue
        if ($c) { return $c.Source }
    }
    $cands = @()
    try {
        $autorun = (Get-ItemProperty "HKCU:\Software\Microsoft\Command Processor" -Name AutoRun -ErrorAction Stop).AutoRun
        $m = [regex]::Match($autorun, '"([^"]*clink[^"]*\.(exe|bat))"', "IgnoreCase")
        if ($m.Success) { $cands += (Join-Path (Split-Path $m.Groups[1].Value -Parent) "clink_x64.exe") }
    } catch { }
    $cands += @(
        (Join-Path $env:LOCALAPPDATA "Programs\clink\clink_x64.exe"),
        (Join-Path $env:ProgramFiles "clink\clink_x64.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "clink\clink_x64.exe")
    )
    foreach ($p in $cands) { if ($p -and (Test-Path $p)) { return $p } }
    return $null
}

# 目标目录 = clink 自己报告的 state（会话实际使用的 profile 目录）。
# 注意：Clink 只加载脚本目录"当层"的 *.lua，子目录不会被递归加载（只有 completions\
# 是特例且为按需加载），所以插件必须直接放进该目录。
$ClinkExe = Resolve-ClinkExe
$ClinkDirs = @()
if ($ClinkExe) {
    $stateLine = & $ClinkExe info 2>$null | Where-Object { $_ -match "^\s*state\s*:" } | Select-Object -First 1
    $state = if ($stateLine) { ($stateLine -replace "^\s*state\s*:", "").Trim() } else { "" }
    # Clink 不展开 --profile 里的 "~"（实测会变成相对路径），只接受绝对路径
    if ($state -and [System.IO.Path]::IsPathRooted($state)) { $ClinkDirs += $state }
}
$ClinkFallback = Join-Path $env:LOCALAPPDATA "clink"
if ($ClinkDirs.Count -eq 0) { $ClinkDirs += $ClinkFallback }

$LuaHash = (Get-FileHash $LuaSrc -Algorithm SHA256).Hash
foreach ($dir in $ClinkDirs) {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $dstLua = Join-Path $dir "cmdpilot.lua"
    $dstExe = Join-Path $dir "cmdpilot-clink.exe"
    Copy-Item $LuaSrc $dstLua -Force
    if (Test-Path $CompanionExe) {
        Copy-Item $CompanionExe $dstExe -Force
    } else {
        Write-Warn2 "未找到 $CompanionExe，CMD 补全不可用（请先构建，或去掉 -SkipBuild）"
    }
    # 复制后必须校验落地：此前不看结果就打印成功，实际没复制过去
    if (-not (Test-Path $dstLua)) { throw "Clink 插件复制失败：$dstLua" }
    if ((Get-FileHash $dstLua -Algorithm SHA256).Hash -ne $LuaHash) { throw "Clink 插件复制后内容不一致：$dstLua" }
    if ((Test-Path $CompanionExe) -and -not (Test-Path $dstExe)) { throw "伴侣进程复制失败：$dstExe" }
    Write-OK "Clink 插件已安装到 $dir（cmdpilot.lua + cmdpilot-clink.exe）"
}

# Clink 未安装？引导但不报错退出
if (-not $ClinkExe) {
    Write-Warn2 "未检测到 Clink。CMD 补全需要 Clink，可稍后执行："
    Write-Host "        winget install clink" -ForegroundColor White
    Write-Host "        或从 https://chrisant996.github.io/clink/ 下载官方安装包" -ForegroundColor White
    Write-Host "        （安装 Clink 后 CMD 自动加载 $ClinkFallback 下的插件）" -ForegroundColor White
} else {
    # 诊断：AutoRun 里出现相对 --profile 时，Clink 不展开 ~，会在每个 cmd 启动目录下新建该目录
    try {
        $autorun = (Get-ItemProperty "HKCU:\Software\Microsoft\Command Processor" -Name AutoRun -ErrorAction Stop).AutoRun
        $pm = [regex]::Match($autorun, '--profile\s+"?([^"\s]+)"?')
        if ($pm.Success -and -not [System.IO.Path]::IsPathRooted($pm.Groups[1].Value)) {
            Write-Warn2 "cmd AutoRun 的 --profile 是相对路径（$($pm.Groups[1].Value)）：Clink 不展开 ~，会在每个 cmd 启动目录下新建该目录。"
            Write-Warn2 "建议改成绝对路径 $ClinkFallback（当前会话实际使用的 profile 目录）。"
        }
    } catch { }
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
