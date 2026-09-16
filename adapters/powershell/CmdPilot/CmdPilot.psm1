#Requires -Version 5.1
Set-StrictMode -Version 2.0

# ============================================================================
# CmdPilot - PSReadLine Predictor 适配层（公共部分）
#
# 架构（详见 ADR-002）：
#   * 核心引擎以独立守护进程运行；本模块通过 cmdpilot-clink.exe 伴侣二进制
#     与守护进程通信（JSON 请求/输出文件模式），模块不 import 任何 core 逻辑，
#     Remove-Module 后无残留。
#   * 建议计算完全异步：后台线程按 debounce（默认 300ms）拉取快照，
#     GetSuggestion 只读快照，绝不阻塞键盘输入；AI 增强由守护进程异步完成
#     并缓存（同前缀 5 分钟）。
#   * 预测器插件 API 只有一代（引擎级）：System.Management.Automation.
#     Subsystem.Prediction.ICommandPredictor，仅 PS 7.4+ / PSReadLine 2.3.4+
#     的宿主引擎提供，经 SubsystemManager 注册。PredictorNew.ps1 定义
#     CmdPilotPredictor 类（继承 CmdPilotPredictorBase），仅在探测到该 API
#     时 dot-source。
#   * PS 5.1（及 PS 7.0-7.3）无任何可用的预测器插件 API——PSReadLine 2.2.x
#     无 ICommandPredictor/RegisterPredictor（已按 2.2.5 二进制与源码实证），
#     2.3+ 的引擎 Subsystem 类型在旧宿主不存在。探测结果为 'none'：不加载
#     预测器类，退化为 Tab 补全（见 Enable-CmdPilot），模块仍可用而不报错。
# ============================================================================

# ---- 路径与全局状态 ------------------------------------------------------
if ($env:LOCALAPPDATA) {
    $script:CmdPilotDataDir = Join-Path $env:LOCALAPPDATA 'CmdPilot'
} else {
    $script:CmdPilotDataDir = Join-Path (Join-Path $HOME '.local\share') 'cmdpilot'
}
$script:CmdPilotConfigPath = Join-Path $script:CmdPilotDataDir 'config.json'
$script:CmdPilotBinDir = Join-Path $PSScriptRoot 'bin'
$script:CmdPilotCompanion = Join-Path $script:CmdPilotBinDir 'cmdpilot-clink.exe'
if (-not (Test-Path -LiteralPath $script:CmdPilotCompanion)) {
    $script:CmdPilotCompanion = Join-Path $script:CmdPilotBinDir 'cmdpilot-clink'
}
if (-not (Test-Path -LiteralPath $script:CmdPilotCompanion)) {
    $script:CmdPilotCompanion = 'cmdpilot-clink'
}
$script:CmdPilotMain = Join-Path $script:CmdPilotBinDir 'cmdpilot.exe'
if (-not (Test-Path -LiteralPath $script:CmdPilotMain)) {
    $script:CmdPilotMain = Join-Path $script:CmdPilotBinDir 'cmdpilot'
}
if (-not (Test-Path -LiteralPath $script:CmdPilotMain)) {
    $script:CmdPilotMain = 'cmdpilot'
}
$script:CmdPilotPredictor = $null
$script:CmdPilotApiKind = 'none'     # 'new' | 'none'（无引擎级预测器 API 时退化为 Tab 补全）
$script:CmdPilotPSRStatic = $null    # PSReadLine 静态类（版本探测后缓存）
$script:CmdPilotTabFallback = $false # 无预测器 API 时已启用 Tab 降级补全
$script:CmdPilotPromptWrapped = $false
$script:CmdPilotOriginalPrompt = $null
$script:CmdPilotLastHistoryCount = 0

# ---- 配置读取（只读，损坏即忽略） ------------------------------------------
function Get-CmdPilotConfig {
    if (-not (Test-Path -LiteralPath $script:CmdPilotConfigPath)) { return $null }
    try {
        $raw = Get-Content -LiteralPath $script:CmdPilotConfigPath -Raw -ErrorAction Stop
        return ($raw | ConvertFrom-Json -ErrorAction Stop)
    } catch {
        return $null
    }
}

# ---- 伴侣进程调用（供 Tab 模式等公共命令使用） ------------------------------
function Start-CmdPilotProcess {
    <#
    .SYNOPSIS
    以 CreateNoWindow 方式启动可执行文件并等待退出，返回退出码（失败返回 -1）。
    # 不用 ProcessStartInfo.ArgumentList —— PS 5.1 + .NET Framework 运行时缺失该
    # 属性（PropertyNotFoundException，本机实证），改为手工拼参数串并逐参加引号。
    # 参数名不能用 $Args（与自动变量 $args 冲突，实测绑定后取到空数组）。
    #>
    param(
        [string] $Exe,
        [string[]] $ArgList,
        [int] $TimeoutMs = 0
    )
    try {
        $psi = [System.Diagnostics.ProcessStartInfo]::new()
        $psi.FileName = $Exe
        # .NET 命令行解析规则：参数用双引号包裹，参数内双引号以 "" 转义
        $psi.Arguments = ($ArgList | ForEach-Object { '"' + $_.Replace('"', '""') + '"' }) -join ' '
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $p = [System.Diagnostics.Process]::new()
        $p.StartInfo = $psi
        if (-not $p.Start()) { return -1 }
        if ($TimeoutMs -gt 0) {
            if (-not $p.WaitForExit($TimeoutMs)) {
                try { $p.Kill() } catch { }
                return -1
            }
        } else {
            $p.WaitForExit() | Out-Null
        }
        return $p.ExitCode
    } catch {
        return -1
    }
}

function Invoke-CmdPilotCompanion {
    param(
        [hashtable] $Request,
        [int] $TimeoutMs = 2000
    )
    try {
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-ps-' + [guid]::NewGuid().ToString('N') + '.json')
        $out = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-ps-' + [guid]::NewGuid().ToString('N') + '.out.json')
        $json = $Request | ConvertTo-Json -Compress -Depth 5
        [System.IO.File]::WriteAllText($tmp, $json)
        $code = Start-CmdPilotProcess -Exe $script:CmdPilotCompanion `
            -ArgList @('--request', $tmp, '--output', $out) -TimeoutMs $TimeoutMs
        if ($code -lt 0) { return $null }
        if (-not (Test-Path -LiteralPath $out)) { return $null }
        $obj = [System.IO.File]::ReadAllText($out) | ConvertFrom-Json
        # 模块开了 Set-StrictMode 2.0：裸属性访问不存在的字段会抛
        # PropertyNotFoundException，必须经 PSObject.Properties 探测。
        $errProp = $obj.PSObject.Properties['error']
        if ($null -ne $errProp -and $null -ne $errProp.Value) { return $null }
        return $obj
    } catch {
        return $null
    } finally {
        if ($tmp -and (Test-Path -LiteralPath $tmp)) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
        if ($out -and (Test-Path -LiteralPath $out)) { Remove-Item -LiteralPath $out -Force -ErrorAction SilentlyContinue }
    }
}

function Send-CmdPilotReport {
    param([string] $Command, [string] $Dir, [string] $Shell)
    if (-not $Command) { return }
    try {
        Start-CmdPilotProcess -Exe $script:CmdPilotCompanion `
            -ArgList @('--report', $Command, '--report-dir', $Dir, '--report-shell', $Shell) -TimeoutMs 1000 | Out-Null
    } catch { }
}

# ---- PSReadLine 静态 API 兼容层 -------------------------------------------
# PSReadLine 2.3.0 起把静态类 Microsoft.PowerShell.PSReadLine.PSReadLine 更名为
# Microsoft.PowerShell.PSConsoleReadLine，并移除 GetLineState()（改为
# GetBufferState([ref],[ref])）。以下函数按版本解析正确的类型与入口，使 Tab
# 补全在 PSReadLine 2.2.x / 2.3+ 上都能工作。
function Resolve-CmdPilotPSReadLineStatic {
    <#
    .SYNOPSIS
    返回提供当前缓冲静态方法的 PSReadLine 类型（2.3+ 为 PSConsoleReadLine，2.2 为 PSReadLine.PSReadLine）。
    #>
    if ($script:CmdPilotPSRStatic) { return $script:CmdPilotPSRStatic }
    $asm = [AppDomain]::CurrentDomain.GetAssemblies() | Where-Object { $_.GetName().Name -eq 'Microsoft.PowerShell.PSReadLine' }
    if ($null -ne $asm) {
        foreach ($n in @('Microsoft.PowerShell.PSConsoleReadLine', 'Microsoft.PowerShell.PSReadLine.PSReadLine')) {
            $t = $asm.GetType($n)
            if ($null -ne $t) {
                $script:CmdPilotPSRStatic = $t
                return $t
            }
        }
    }
    return $null
}

function Get-CmdPilotLineState {
    <#
    .SYNOPSIS
    读取当前输入行：PSReadLine 2.3+ 用 GetBufferState([ref])，2.2 用 GetLineState()。
    #>
    $line = ''
    $psr = Resolve-CmdPilotPSReadLineStatic
    if ($null -eq $psr) { return $line }
    # PSReadLine 2.3+ 的 GetBufferState 有两个重载（无参与 out string/out int），
    # 必须按参数类型精确选型，否则 GetMethod 抛 AmbiguousMatchException。
    $gm = $psr.GetMethod('GetBufferState', [type[]]@([string].MakeByRefType(), [int].MakeByRefType()))
    if ($null -ne $gm) {
        $cursor = 0
        try { $psr::GetBufferState([ref]$line, [ref]$cursor) } catch { $line = '' }
    } else {
        try {
            $ls = $psr::GetLineState()
            if ($null -ne $ls) { $line = [string]$ls.Buffer }
        } catch { $line = '' }
    }
    return $line
}

function Get-CmdPilotBuffer {
    <#
    .SYNOPSIS
    读取当前输入行与光标位置。Tab 接受建议前必须知道光标是否在行尾：建议是整行的
    后缀，插入发生在光标处，光标在行中时插入会破坏输入。取不到时 cursor = -1
    （调用方按"不可接受"处理，退回原生菜单）。
    #>
    $r = @{ line = ''; cursor = -1 }
    $psr = Resolve-CmdPilotPSReadLineStatic
    if ($null -eq $psr) { return $r }
    # 与 Get-CmdPilotLineState 同理：GetBufferState 有重载，反射取型避免歧义。
    $gm = $psr.GetMethod('GetBufferState', [type[]]@([string].MakeByRefType(), [int].MakeByRefType()))
    if ($null -eq $gm) { return $r }
    $line = ''
    $cursor = 0
    try {
        $psr::GetBufferState([ref]$line, [ref]$cursor)
        $r.line = [string]$line
        $r.cursor = [int]$cursor
    } catch { }
    return $r
}

function Set-CmdPilotTabKey {
    <#
    .SYNOPSIS
    绑定/还原 Tab 键：-AiTab 时按 Tab 调 companion 补全，否则还原为 PSReadLine 默认 MenuComplete。
    #>
    [CmdletBinding()]
    param([switch] $AiTab)
    if ($AiTab) {
        Set-PSReadLineKeyHandler -Key Tab -BriefDescription 'CmdPilotAITab' -ScriptBlock {
            $line = Get-CmdPilotLineState
            $req = @{ input = $line; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'tab' }
            $result = Invoke-CmdPilotCompanion -Request $req -TimeoutMs 1500
            $tp = $null
            if ($result) { $tp = $result.PSObject.Properties['top'] }
            $psr = Resolve-CmdPilotPSReadLineStatic
            if ($tp -and $tp.Value -and $tp.Value.PSObject.Properties['text'] -and $psr) {
                $psr::Insert([string]$tp.Value.text)
            } elseif ($psr) {
                $psr::MenuComplete($args[0], $null)
            }
        }
    } else {
        # 幽灵文本已出现时 Tab = 接受（把快照里的后缀插入当前行）；没有才退回 PSReadLine
        # 原生候选菜单。快照是 worker 早已取好的结果，接受零成本、不起进程、不阻塞
        # （此前 Tab 固定绑 MenuComplete：只弹列表、不落字，对灰色建议等于"无效"，
        #  与本模块 README 承诺的"按 Tab 或 → 接受"不一致）。
        # 列表菜单仍在 PSReadLine 默认键位上（Ctrl+Space / Ctrl+@）。
        Set-PSReadLineKeyHandler -Key Tab -BriefDescription 'CmdPilotAcceptOrMenu' -ScriptBlock {
            $psr = Resolve-CmdPilotPSReadLineStatic
            if (-not $psr) { return }
            $buf = Get-CmdPilotBuffer
            $line = [string]$buf['line']
            $p = $script:CmdPilotPredictor
            # 光标在行尾才接受：建议是整行后缀，插入点即光标，光标在行中会插错位置。
            if ($p -and $line -and $buf['cursor'] -eq $line.Length) {
                $peek = $p.PeekSuggestion($line)
                if ($peek['ready'] -and $peek['text']) {
                    $psr::Insert([string]$peek['text'])
                    return
                }
            }
            $psr::MenuComplete($args[0], $null)
        }
    }
}

function Enable-CmdPilotPredictionOptions {
    <#
    .SYNOPSIS
    确保 PSReadLine 真正启用预测器：PredictionSource 含 Plugin、PredictionView 为 InlineView。
    #>
    # 注册 ICommandPredictor 只是让引擎"知道"存在预测器；PSReadLine 仅当
    # PredictionSource 含 Plugin 时才会调用它。默认可能是 None/History——此时注册了
    # 也毫无效果：无幽灵文本、Tab 的 MenuComplete 也没有候选。必须显式开启。
    # 尽力而为：非交互 / 不支持 VT 的宿主会抛错，静默忽略（不影响模块加载）。
    try {
        $opt = Get-PSReadLineOption -ErrorAction Stop
        $ps = $opt.PSObject.Properties['PredictionSource']
        if ($ps) {
            $v = [string]$ps.Value
            if ($v -ne 'Plugin' -and $v -ne 'HistoryAndPlugin') {
                Set-PSReadLineOption -PredictionSource HistoryAndPlugin -ErrorAction Stop
            }
        }
        $pv = $opt.PSObject.Properties['PredictionView']
        if ($pv -and [string]$pv.Value -ne 'InlineView') {
            Set-PSReadLineOption -PredictionView InlineView -ErrorAction Stop
        }
    } catch {
        Write-Verbose "CmdPilot: 启用 PSReadLine 预测器失败（宿主可能非交互）: $($_.Exception.Message)"
    }
}

# ============================================================================
# 公共基类：状态、后台 worker（debounce + 快照）、伴侣调用
# ============================================================================
class CmdPilotPredictorBase {
    # 跨 runspace 共享状态：后台 worker 与 GetSuggestion 都在一个进程内，
    # 通过同一 hashtable 引用 + Monitor 锁交换数据（in-proc 不序列化）。
    hidden [hashtable] $Sync
    hidden [System.Management.Automation.Runspaces.Runspace] $WorkerRS = $null
    hidden [System.Management.Automation.PowerShell] $WorkerPS = $null
    hidden [System.IAsyncResult] $WorkerHandle = $null
    hidden [string] $CompanionPath = ''

    CmdPilotPredictorBase() {
        $this.CompanionPath = $script:CmdPilotCompanion
        $this.Sync = @{
            lock     = [object]::new()
            pending  = ''
            gen      = 0
            snapInput = ''
            snapTop  = $null
            snapFull = ''
            snapList = [System.Collections.Generic.List[object]]::new()
            running  = $false
            tabOnly  = $false
        }
    }

    [bool] IsRunning() { return [bool]$this.Sync.running }

    [void] SetTabOnly([bool] $v) { $this.Sync.tabOnly = $v }
    [bool] GetTabOnly() { return [bool]$this.Sync.tabOnly }

    # 只读查询：当前快照是否已对 $input 算好（供 Tab 接受内联建议用）。
    # 不登记新一轮输入、不起进程——worker 早已取好的结果直接复用，故接受是零成本。
    [hashtable] PeekSuggestion([string] $input) {
        $s = $this.Sync
        $r = @{ ready = $false; text = ''; full = '' }
        [System.Threading.Monitor]::Enter($s.lock)
        try {
            if ($s.snapInput -eq $input -and $null -ne $s.snapTop) {
                $tp = $s.snapTop.PSObject.Properties['text']
                if ($tp -and -not [string]::IsNullOrEmpty([string]$tp.Value)) {
                    $r.ready = $true
                    $r.text = [string]$tp.Value
                    $r.full = [string]$s.snapFull
                }
            }
        } finally {
            [System.Threading.Monitor]::Exit($s.lock)
        }
        return $r
    }

    # 登记新一轮输入并返回当前快照（只读锁内拷贝）。
    hidden [hashtable] RegisterAndGetSnapshot([string] $input) {
        $s = $this.Sync
        $needWorker = $false
        $snap = @{ current = $false; top = $null; full = ''; list = @() }
        [System.Threading.Monitor]::Enter($s.lock)
        try {
            $snap.top = $s.snapTop
            $snap.full = $s.snapFull
            $snap.list = @($s.snapList)
            if ($s.snapInput -eq $input) {
                $snap.current = $true
            }
            if (-not $s.tabOnly) {
                $s.pending = $input
                $s.gen += 1
            }
            $needWorker = ($null -eq $this.WorkerPS -and -not $s.running)
        } finally {
            [System.Threading.Monitor]::Exit($s.lock)
        }
        if ($needWorker) { $this.StartWorker() }
        return $snap
    }

    hidden [void] StartWorker() {
        # 专用 runspace：PowerShell scriptblock 不能在 raw .NET 线程执行。
        # worker 是纯脚本（不调类方法），通过共享 hashtable 读写状态，
        # 避免跨 runspace 类方法/隐藏成员可见性问题。
        if ($null -ne $this.WorkerPS) { return }
        $this.Sync.running = $true
        $rs = [runspacefactory]::CreateRunspace()
        $rs.Open()
        $this.WorkerRS = $rs
        $ps = [powershell]::Create()
        $ps.Runspace = $rs
        $this.WorkerPS = $ps
        $state = $this.Sync
        $cmp = $this.CompanionPath
        $sb = {
            param($s, $companion)
            # 已经为哪一代输入取过结果。写完快照并不改变 gen/pending，若无此标记，
            # 下一轮循环会判定"输入没变"从而对**同一个输入**再次起进程，形成永久空转
            # 重取——实测空闲 6 秒仍触发 18 次 companion 进程创建（每次约 22ms 进程创建
            # + 两个临时文件 + Defender 扫描），是窗口卡顿/输入延迟的根源。每代只取一次。
            $lastGen = -1
            while ($true) {
                Start-Sleep -Milliseconds 150
                if (-not $s.running) { break }
                $gen = 0
                $input = ''
                [System.Threading.Monitor]::Enter($s.lock)
                try {
                    $gen = $s.gen
                    $input = $s.pending
                } finally {
                    [System.Threading.Monitor]::Exit($s.lock)
                }
                if ([string]::IsNullOrEmpty($input)) { continue }
                if ($gen -eq $lastGen) { continue }   # 本代已取过：不再起进程
                Start-Sleep -Milliseconds 150   # debounce：确认输入稳定
                $stillCurrent = $false
                [System.Threading.Monitor]::Enter($s.lock)
                try {
                    $stillCurrent = ($gen -eq $s.gen)
                } finally {
                    [System.Threading.Monitor]::Exit($s.lock)
                }
                if (-not $stillCurrent) { continue }
                # 取之前先记账：本代即使取失败（守护进程不可用/超时）也不再重试，
                # 避免失败时形成重试风暴；下一次按键会产生新 gen，届时自然重试。
                $lastGen = $gen

                # inline 伴侣调用（纯脚本，不依赖模块函数/类方法）
                $req = @{ input = $input; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'auto' }
                $json = $req | ConvertTo-Json -Compress -Depth 5
                $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.json')
                $out = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.out.json')
                try {
                    [System.IO.File]::WriteAllText($tmp, $json)
                    # 不用 ArgumentList（PS 5.1 缺失该属性）：手工拼参数串
                    $argList = @('--request', $tmp, '--output', $out) |
                        ForEach-Object { '"' + $_.Replace('"', '""') + '"' }
                    $psi = [System.Diagnostics.ProcessStartInfo]::new()
                    $psi.FileName = $companion
                    $psi.Arguments = $argList -join ' '
                    $psi.UseShellExecute = $false
                    $psi.CreateNoWindow = $true
                    $p = [System.Diagnostics.Process]::new()
                    $p.StartInfo = $psi
                    if ($p.Start() -and $p.WaitForExit(2000)) {
                        if (Test-Path -LiteralPath $out) {
                            $obj = [System.IO.File]::ReadAllText($out) | ConvertFrom-Json
                            [System.Threading.Monitor]::Enter($s.lock)
                            try {
                                if ($gen -eq $s.gen) {
                                    $s.snapInput = $input
                                    $s.snapTop = $obj.top
                                    if ($obj.top) { $s.snapFull = [string]$obj.top.full }
                                    $s.snapList.Clear()
                                    foreach ($it in @($obj.list)) {
                                        if ($it) { $s.snapList.Add($it) }
                                    }
                                }
                            } finally {
                                [System.Threading.Monitor]::Exit($s.lock)
                            }
                        }
                    }
                } catch {
                    # 守护进程不可用/超时/解析失败：静默降级，保留旧快照
                } finally {
                    if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
                    if (Test-Path -LiteralPath $out) { Remove-Item -LiteralPath $out -Force -ErrorAction SilentlyContinue }
                }
            }
        }
        $this.WorkerHandle = $ps.AddScript($sb).AddArgument($state).AddArgument($cmp).BeginInvoke()
    }

    hidden [void] StopWorker() {
        $s = $this.Sync
        [System.Threading.Monitor]::Enter($s.lock)
        try { $s.running = $false } finally { [System.Threading.Monitor]::Exit($s.lock) }
        if ($null -ne $this.WorkerPS) {
            try { $this.WorkerPS.Stop() } catch { }
            try { $this.WorkerHandle.WaitOne(500) | Out-Null } catch { }
            try { $this.WorkerPS.Dispose() } catch { }
            $this.WorkerPS = $null
        }
        if ($null -ne $this.WorkerRS) {
            try { $this.WorkerRS.Close() } catch { }
            try { $this.WorkerRS.Dispose() } catch { }
            $this.WorkerRS = $null
        }
    }
}

# ============================================================================
# API 探测与实现加载：只 dot-source 当前主机可用的一份（PredictorNew.ps1）。
# 预测器插件 API 只有引擎级 Subsystem 一种（PS 7.4+ / PSReadLine 2.3.4+）；
# PS 5.1 / PS 7.0-7.3 探测恒为 'none'：不加载任何预测器类，退化为 Tab 补全
# （见 Enable-CmdPilot），模块仍可用而不报错。
# ============================================================================
function Get-CmdPilotPSReadLineModule {
    # 画廊版模块名是 PSReadLine；Windows 11 内建版也叫 PSReadLine（位于
    # Program Files\WindowsPowerShell\Modules\PSReadLine\2.0.0），只有个别旧系统
    # 才叫 Microsoft.PowerShell.PSReadLine。按名称逐一探测，取最高版本。
    return Get-Module -ListAvailable |
        Where-Object { $_.Name -in @('PSReadLine', 'Microsoft.PowerShell.PSReadLine') } |
        Sort-Object Version -Descending | Select-Object -First 1
}

function Test-CmdPilotPredictorApi {
    # 仅探测引擎级 Subsystem API（PS 7.4+ / PSReadLine 2.3.4+ 的宿主引擎提供）。
    # 注意：PS 5.1 上任何 PSReadLine 版本都没有插件预测器 API——2.2.x 无
    # ICommandPredictor/RegisterPredictor（2.2.5 二进制与源码实证，且
    # -PredictionSource Plugin 在 .NET Framework 上直接抛异常），2.3+ 的
    # Subsystem 类型仅在 PS 7.4 引擎中存在。故 5.1 / 7.0-7.3 恒为 'none'。
    # 探测方式修正（2026-09-16 实测）：不能用 [type]::GetType('...') —— 它只查
    # 调用程序集与核心库，不扫全部已加载程序集；pwsh 7.6.6 下该接口明明存在于
    # System.Management.Automation.dll 却返回 $null，导致误判 'none'（永远走 Tab
    # 降级）。改用 PowerShell 类型字面量：解析器扫描全部已加载程序集，
    # 7.4+ 命中 → 'new'；5.1 / 7.0-7.3 未命中抛"无法找到类型"被捕获 → 'none'。
    try {
        $null = [System.Management.Automation.Subsystem.Prediction.ICommandPredictor]
        return 'new'
    } catch {
        return 'none'
    }
}

$script:CmdPilotApiKind = Test-CmdPilotPredictorApi
if ($script:CmdPilotApiKind -eq 'new') {
    . (Join-Path $PSScriptRoot 'PredictorNew.ps1')
}

function Enable-CmdPilotPromptStats {
    # 'none' 降级无"命令已执行"事件：用 prompt 钩子补统计
    # （新 API 用 OnCommandLineExecuted）。幂等。
    if ($script:CmdPilotPromptWrapped) { return }
    $script:CmdPilotOriginalPrompt = $function:prompt
    $script:CmdPilotLastHistoryCount = @(Get-History).Count
    $function:prompt = {
        try {
            $h = @(Get-History)
            if ($h.Count -gt $script:CmdPilotLastHistoryCount) {
                $new = @($h | Select-Object -Skip $script:CmdPilotLastHistoryCount)
                $script:CmdPilotLastHistoryCount = $h.Count
                foreach ($c in $new) {
                    if ($c.CommandLine) {
                        Send-CmdPilotReport -Command $c.CommandLine -Dir (Get-Location).Path -Shell 'ps'
                    }
                }
            }
        } catch { }
        & $script:CmdPilotOriginalPrompt
    }
    $script:CmdPilotPromptWrapped = $true
}

# ============================================================================
# 公共命令
# ============================================================================
function Enable-CmdPilot {
    <#
    .SYNOPSIS
    注册 CmdPilot Predictor（inline 幽灵文本 + 列表视图）。
    .PARAMETER Mode
    Auto：自动建议（默认，AI 异步增强）；Tab：仅 Tab 触发 AI 补全。
    .PARAMETER Silent
    不打印启用提示行。
    #>
    [CmdletBinding()]
    param(
        [ValidateSet('Auto', 'Tab')] [string] $Mode = 'Auto',
        [switch] $Silent
    )
    if ($script:CmdPilotPredictor) {
        Write-Verbose 'CmdPilot: already enabled'
        return
    }
    # 无引擎级预测器 API 的主机（PS 5.1 / PS 7.0-7.3，任何 PSReadLine 版本均无
    # 插件 API）：退化为 Tab 补全，不创建预测器类。
    if ($script:CmdPilotApiKind -eq 'none') {
        if ($script:CmdPilotTabFallback) { return }
        $psr = Get-CmdPilotPSReadLineModule
        if (-not $psr -or $psr.Version -lt [version]'2.2.0') {
            Write-Warning 'CmdPilot: 需要 PSReadLine >= 2.2（运行: Install-Module PSReadLine -Force -Scope CurrentUser），未启用。'
            return
        }
        if (-not (Get-Module PSReadLine) -and -not (Get-Module Microsoft.PowerShell.PSReadLine)) {
            try { Import-Module $psr.Name -ErrorAction Stop } catch {
                Write-Warning 'CmdPilot: 无法加载 PSReadLine，未启用。'
                return
            }
        }
        Set-CmdPilotTabKey -AiTab
        $script:CmdPilotTabFallback = $true
        Enable-CmdPilotPromptStats
        if (-not $Silent) {
            Write-Host 'CmdPilot 已启用 [Tab 降级] — 当前宿主（PS 5.1）无预测器插件 API，Tab 触发本地/AI 补全可用；inline 幽灵文本需 PowerShell 7.4+（cmdpilot help）' -ForegroundColor Yellow
        }
        return
    }
    $predictor = [CmdPilotPredictor]::new()
    $predictor.SetTabOnly($Mode -eq 'Tab')
    try {
        # 幂等：同名 Id 若已注册（-Force 重载 / 重复 Import-Module），先注销再注册，
        # 使新实例成为生效实例，避免"already registered"告警与脚本变量指向失效实例。
        [System.Management.Automation.Subsystem.SubsystemManager]::UnregisterSubsystem(
            [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor, [guid]'7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f')
    } catch { }
    try {
        [System.Management.Automation.Subsystem.SubsystemManager]::RegisterSubsystem(
            [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor, $predictor)
    } catch {
        Write-Warning "CmdPilot: 注册 Predictor 失败: $($_.Exception.Message)"
        return
    }
    $script:CmdPilotPredictor = $predictor
    # 注册预测器 ≠ 被调用：须让 PSReadLine 的 PredictionSource 含 Plugin，否则无幽灵文本。
    Enable-CmdPilotPredictionOptions
    if ($Mode -eq 'Tab') {
        Set-CmdPilotTabKey -AiTab
    } else {
        Set-CmdPilotTabKey
    }
    # 'none' 降级无"命令已执行"事件：用 prompt 钩子补统计（新 API 用 OnCommandLineExecuted）。
    Enable-CmdPilotPromptStats
    if (-not $Silent) {
        $cfg = Get-CmdPilotConfig
        $engine = 'hybrid'
        $show = $true
        if ($cfg) {
            $ep = $cfg.PSObject.Properties['engine']
            if ($ep) { $engine = [string]$ep.Value }
            $pp = $cfg.PSObject.Properties['enable_prompt_line']
            if ($pp) { $show = [bool]$pp.Value }
        }
        if ($show) {
            Write-Host "CmdPilot 已启用 [$engine/$Mode] — 幽灵文本 + 列表补全 (本地库+AI+收藏+频率推荐), 输入 cmdpilot help 查看命令" -ForegroundColor DarkGray
        }
    }
}

function Disable-CmdPilot {
    <#
    .SYNOPSIS
    注销 Predictor、停止后台线程、还原 prompt 钩子与 Tab 键。
    #>
    [CmdletBinding()]
    param()
    if ($script:CmdPilotPredictor) {
        $script:CmdPilotPredictor.StopWorker()
        try {
            # UnregisterSubsystem 以实现 Id（Guid）为参，不以实例为参。
            [System.Management.Automation.Subsystem.SubsystemManager]::UnregisterSubsystem(
                [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor, [guid]'7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f')
        } catch {
            Write-Verbose "CmdPilot: unregister failed: $($_.Exception.Message)"
        }
        $script:CmdPilotPredictor = $null
    }
    if ($script:CmdPilotPromptWrapped) {
        $function:prompt = $script:CmdPilotOriginalPrompt
        $script:CmdPilotPromptWrapped = $false
    }
    $script:CmdPilotTabFallback = $false
    Set-PSReadLineKeyHandler -Key Tab -Function MenuComplete -ErrorAction SilentlyContinue
    Write-Host 'CmdPilot 已禁用（当前会话）。重启会话可完全清除。' -ForegroundColor DarkGray
}

function Set-CmdPilotMode {
    <#
    .SYNOPSIS
    切换触发模式：Auto（自动建议+AI 异步增强）或 Tab（仅 Tab 触发 AI）。
    #>
    [CmdletBinding()]
    param([ValidateSet('Auto', 'Tab')] [string] $Mode)
    if (-not $script:CmdPilotPredictor) {
        Enable-CmdPilot -Mode $Mode
        return
    }
    $script:CmdPilotPredictor.SetTabOnly($Mode -eq 'Tab')
    if ($Mode -eq 'Tab') {
        Set-CmdPilotTabKey -AiTab
    } else {
        Set-CmdPilotTabKey
    }
    Write-Host "CmdPilot 模式: $Mode"
}

function Get-CmdPilotStatus {
    <#
    .SYNOPSIS
    查看启用状态、模式、配置与守护进程健康。
    #>
    [CmdletBinding()]
    param()
    $mode = if ($script:CmdPilotPredictor) {
        if ($script:CmdPilotPredictor.GetTabOnly()) { 'Tab' } else { 'Auto' }
    } elseif ($script:CmdPilotTabFallback) {
        'Tab 降级'
    } else { '未启用' }
    Write-Host "Predictor: $mode  (API: $script:CmdPilotApiKind)" -ForegroundColor Cyan
    $cfg = Get-CmdPilotConfig
    if ($cfg) {
        $engine = 'hybrid'; $trigger = 'auto'
        $ep = $cfg.PSObject.Properties['engine']; if ($ep) { $engine = [string]$ep.Value }
        $tp = $cfg.PSObject.Properties['trigger']; if ($tp) { $trigger = [string]$tp.Value }
        Write-Host "引擎: $engine / 触发: $trigger"
        $ai = $cfg.PSObject.Properties['ai']
        if ($ai) {
            $base = $ai.Value.PSObject.Properties['base_url']
            $model = $ai.Value.PSObject.Properties['model']
            if ($base) {
                $m = if ($model) { [string]$model.Value } else { '-' }
                Write-Host "AI 端点: $($base.Value)  (model: $m)"
            } else {
                Write-Host 'AI 端点: 未配置（本地模式可用）'
            }
        } else {
            Write-Host 'AI 端点: 未配置（本地模式可用）'
        }
    } else {
        Write-Host '配置: 未找到（守护进程将使用默认配置）'
    }
    & $script:CmdPilotMain daemon status
}

function Sync-CmdPilotHistory {
    <#
    .SYNOPSIS
    手动把 PSReadLine 历史文件中的近期命令导入统计。
    #>
    [CmdletBinding()]
    param([int] $Count = 50)
    $histFile = Join-Path $env:APPDATA 'Microsoft\Windows\PowerShell\PSReadLine\ConsoleHost_history.txt'
    if (-not (Test-Path -LiteralPath $histFile)) {
        Write-Warning "未找到 PSReadLine 历史文件: $histFile"
        return
    }
    $lines = @(Get-Content -LiteralPath $histFile | Where-Object { $_ -and -not $_.StartsWith('#') })
    $lines = @($lines | Select-Object -Last $Count)
    $batch = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-history-' + [guid]::NewGuid().ToString('N') + '.txt')
    try {
        $lines | Set-Content -LiteralPath $batch -Encoding UTF8
        Start-CmdPilotProcess -Exe $script:CmdPilotCompanion `
            -ArgList @('--report-batch', $batch) -TimeoutMs 3000 | Out-Null
        Write-Host "已导入最近 $($lines.Count) 条历史到统计。"
    } finally {
        Remove-Item -LiteralPath $batch -Force -ErrorAction SilentlyContinue
    }
}

function cmdpilot {
    <#
    .SYNOPSIS
    CmdPilot 独立 CLI（配置/收藏/统计/守护进程等）。
    #>
    [CmdletBinding()]
    param([Parameter(ValueFromRemainingArguments = $true)] [string[]] $Args)
    & $script:CmdPilotMain @Args
}

# ---- 模块加载：自动启用（config.enable_prompt_line 控制提示行） -------------
$script:CmdPilotConfig = Get-CmdPilotConfig
if ($script:CmdPilotConfig) {
    $autoMode = if ($script:CmdPilotConfig.trigger -eq 'tab') { 'Tab' } else { 'Auto' }
    Enable-CmdPilot -Mode $autoMode
} else {
    Write-Verbose 'CmdPilot: 未找到配置，未自动启用。'
}
