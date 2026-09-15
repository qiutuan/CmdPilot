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
#   * 双 API 兼容（PSReadLine ≥ 2.2 全覆盖）：
#       - PS 7.4+ / PSReadLine 2.3.4+：引擎级 Subsystem API
#         (System.Management.Automation.Subsystem.Prediction.ICommandPredictor)，
#         经 SubsystemManager 注册；
#       - PS 5.1 / PS 7.0-7.3 + PSReadLine 2.2.x：经典
#         Microsoft.PowerShell.PSReadLine.ICommandPredictor，经
#         [PSReadLine]::RegisterPredictor 注册。
#     PredictorNew.ps1 / PredictorLegacy.ps1 分别定义同名 CmdPilotPredictor
#     类（继承 CmdPilotPredictorBase），按探测结果只 dot-source 一份。
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
$script:CmdPilotApiKind = 'legacy'   # 'new' | 'legacy'
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
        $psi = [System.Diagnostics.ProcessStartInfo]::new()
        $psi.FileName = $script:CmdPilotCompanion
        $psi.ArgumentList.Add('--request'); $psi.ArgumentList.Add($tmp)
        $psi.ArgumentList.Add('--output'); $psi.ArgumentList.Add($out)
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $p = [System.Diagnostics.Process]::new()
        $p.StartInfo = $psi
        if (-not $p.Start()) { return $null }
        if (-not $p.WaitForExit($TimeoutMs)) {
            try { $p.Kill() } catch { }
            return $null
        }
        if (-not (Test-Path -LiteralPath $out)) { return $null }
        $obj = [System.IO.File]::ReadAllText($out) | ConvertFrom-Json
        if ($obj.error) { return $null }
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
        $psi = [System.Diagnostics.ProcessStartInfo]::new()
        $psi.FileName = $script:CmdPilotCompanion
        $psi.ArgumentList.Add('--report'); $psi.ArgumentList.Add($Command)
        $psi.ArgumentList.Add('--report-dir'); $psi.ArgumentList.Add($Dir)
        $psi.ArgumentList.Add('--report-shell'); $psi.ArgumentList.Add($Shell)
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $p = [System.Diagnostics.Process]::new()
        $p.StartInfo = $psi
        if ($p.Start()) { $p.WaitForExit(1000) | Out-Null }
    } catch { }
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
                Start-Sleep -Milliseconds 150   # debounce：确认输入稳定
                $stillCurrent = $false
                [System.Threading.Monitor]::Enter($s.lock)
                try {
                    $stillCurrent = ($gen -eq $s.gen)
                } finally {
                    [System.Threading.Monitor]::Exit($s.lock)
                }
                if (-not $stillCurrent) { continue }

                # inline 伴侣调用（纯脚本，不依赖模块函数/类方法）
                $req = @{ input = $input; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'auto' }
                $json = $req | ConvertTo-Json -Compress -Depth 5
                $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.json')
                $out = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.out.json')
                try {
                    [System.IO.File]::WriteAllText($tmp, $json)
                    $psi = [System.Diagnostics.ProcessStartInfo]::new()
                    $psi.FileName = $companion
                    $psi.ArgumentList.Add('--request'); $psi.ArgumentList.Add($tmp)
                    $psi.ArgumentList.Add('--output'); $psi.ArgumentList.Add($out)
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
# API 探测与实现加载：只 dot-source 当前主机可用的一份
# ============================================================================
$script:CmdPilotApiKind = 'legacy'
try {
    if ($null -ne [System.Management.Automation.Subsystem.Prediction.ICommandPredictor]) {
        $script:CmdPilotApiKind = 'new'
    }
} catch {
    $script:CmdPilotApiKind = 'legacy'
}
if ($script:CmdPilotApiKind -eq 'new') {
    . (Join-Path $PSScriptRoot 'PredictorNew.ps1')
} else {
    . (Join-Path $PSScriptRoot 'PredictorLegacy.ps1')
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
    $predictor = [CmdPilotPredictor]::new()
    $predictor.SetTabOnly($Mode -eq 'Tab')
    try {
        if ($script:CmdPilotApiKind -eq 'new') {
            [System.Management.Automation.Subsystem.SubsystemManager]::RegisterSubsystem(
                [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor, $predictor)
        } else {
            $psr = Get-Module Microsoft.PowerShell.PSReadLine -ListAvailable |
                Sort-Object Version -Descending | Select-Object -First 1
            if (-not $psr -or $psr.Version -lt [version]'2.2.0') {
                Write-Warning 'CmdPilot: 需要 PSReadLine >= 2.2（运行: Install-Module PSReadLine -Force -Scope CurrentUser），未启用。'
                return
            }
            if (-not (Get-Module PSReadLine)) {
                try { Import-Module PSReadLine -ErrorAction Stop } catch {
                    Write-Warning 'CmdPilot: 无法加载 PSReadLine，未启用。'
                    return
                }
            }
            [Microsoft.PowerShell.PSReadLine]::RegisterPredictor($predictor)
        }
    } catch {
        Write-Warning "CmdPilot: 注册 Predictor 失败: $($_.Exception.Message)"
        return
    }
    $script:CmdPilotPredictor = $predictor
    if ($Mode -eq 'Tab') {
        Set-PSReadLineKeyHandler -Key Tab -BriefDescription 'CmdPilotAITab' -ScriptBlock {
            $line = [Microsoft.PowerShell.PSReadLine]::GetLineState() | ForEach-Object { $_.Buffer }
            $req = @{ input = $line; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'tab' }
            $result = Invoke-CmdPilotCompanion -Request $req -TimeoutMs 1500
            $tp = $null
            if ($result) { $tp = $result.PSObject.Properties['top'] }
            if ($tp -and $tp.Value -and $tp.Value.PSObject.Properties['text']) {
                [Microsoft.PowerShell.PSReadLine]::Insert([string]$tp.Value.text)
            } else {
                [Microsoft.PowerShell.PSReadLine]::MenuComplete($args[0], $null)
            }
        }
    } else {
        Set-PSReadLineKeyHandler -Key Tab -Function MenuComplete
    }
    # 旧 API 无"命令已执行"事件：用 prompt 钩子补统计（新 API 用 OnCommandLineExecuted）。
    if ($script:CmdPilotApiKind -eq 'legacy' -and -not $script:CmdPilotPromptWrapped) {
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
            if ($script:CmdPilotApiKind -eq 'new') {
                # UnregisterSubsystem 以实现 Id（Guid）为参，不以实例为参。
                [System.Management.Automation.Subsystem.SubsystemManager]::UnregisterSubsystem(
                    [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor, [guid]'7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f')
            } else {
                try {
                    [Microsoft.PowerShell.PSReadLine]::UnregisterPredictor($script:CmdPilotPredictor)
                } catch {
                    # PSReadLine 2.2 无公开注销 API：predictor 随会话结束自然清除。
                }
            }
        } catch {
            Write-Verbose "CmdPilot: unregister failed: $($_.Exception.Message)"
        }
        $script:CmdPilotPredictor = $null
    }
    if ($script:CmdPilotPromptWrapped) {
        $function:prompt = $script:CmdPilotOriginalPrompt
        $script:CmdPilotPromptWrapped = $false
    }
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
        Set-PSReadLineKeyHandler -Key Tab -BriefDescription 'CmdPilotAITab' -ScriptBlock {
            $line = [Microsoft.PowerShell.PSReadLine]::GetLineState() | ForEach-Object { $_.Buffer }
            $req = @{ input = $line; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'tab' }
            $result = Invoke-CmdPilotCompanion -Request $req -TimeoutMs 1500
            $tp = $null
            if ($result) { $tp = $result.PSObject.Properties['top'] }
            if ($tp -and $tp.Value -and $tp.Value.PSObject.Properties['text']) {
                [Microsoft.PowerShell.PSReadLine]::Insert([string]$tp.Value.text)
            } else {
                [Microsoft.PowerShell.PSReadLine]::MenuComplete($args[0], $null)
            }
        }
    } else {
        Set-PSReadLineKeyHandler -Key Tab -Function MenuComplete
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
        $psi = [System.Diagnostics.ProcessStartInfo]::new()
        $psi.FileName = $script:CmdPilotCompanion
        $psi.ArgumentList.Add('--report-batch'); $psi.ArgumentList.Add($batch)
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $p = [System.Diagnostics.Process]::new()
        $p.StartInfo = $psi
        if ($p.Start()) { $p.WaitForExit(3000) | Out-Null }
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
