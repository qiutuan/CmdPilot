#Requires -Version 7.4
# PredictorNew.ps1 - PS 7.4+ / PSReadLine 2.3.4+ 引擎级 Subsystem API 实现。
# 仅由 CmdPilot.psm1 探测到 System.Management.Automation.Subsystem.Prediction
# 存在时 dot-source（解析时机在探测之后，避免旧主机解析失败）。
#
# 本文件不再包含预测器逻辑，只做三件事：
#   ① 编译并加载预测器核心 PredictorCore.cs（幂等，见 Get-CmdPilotCoreType）；
#   ② New-CmdPilotPredictor / Test-CmdPilotPredictorRegistered；
#   ③ Start-/Stop-CmdPilotWorker：后台取建议（PowerShell + 专用 runspace）。
#
# 为什么预测器本体搬进了 .cs：引擎把预测器回调丢到线程池、只等 20ms，超时即丢弃
# 结果，而 PowerShell 类方法每次都踩满超时（实测 31ms/键，且 predictors=0）。详见
# PredictorCore.cs 头部的实测表。本文件保留的只有"慢一点也无所谓"的 worker——
# 它不在引擎回调线程上。

# ---- ① 预测器核心：编译并加载（幂等） --------------------------------------
function Get-CmdPilotCoreType {
    <#
    .SYNOPSIS
    在已加载程序集里找 CmdPilot.CmdPilotPredictorCore；找不到返回 $null，不抛。
    #>
    # 不用类型字面量、也不用 '...' -as [type]：类型还没加载时两者都会在宿主 $Error
    # 里留记录，而本函数在"首次编译之前"必然要查一次。程序集扫描与
    # Test-CmdPilotPredictorApi 同法——只查、不抛。动态程序集的 GetType 会抛
    # NotSupportedException，先排除；throwOnError = $false 让"本程序集没有该类型"
    # 返回 $null。
    foreach ($asm in [AppDomain]::CurrentDomain.GetAssemblies()) {
        if ($asm.IsDynamic) { continue }
        $t = $asm.GetType('CmdPilot.CmdPilotPredictorCore', $false)
        if ($t) { return $t }
    }
    return $null
}

$script:CmdPilotCoreError = ''
$script:CmdPilotCoreType = Get-CmdPilotCoreType
if (-not $script:CmdPilotCoreType) {
    # 幂等：模块重载（Import-Module -Force）时程序集已在进程里，上面的查询直接命中，
    # 不再重复编译（重复 Add-Type 同一份源码会告警/报重复类型）。
    $script:CmdPilotCoreSource = Join-Path $PSScriptRoot 'PredictorCore.cs'
    if (Test-Path -LiteralPath $script:CmdPilotCoreSource) {
        try {
            Add-Type -Path $script:CmdPilotCoreSource -ErrorAction Stop
        } catch {
            $script:CmdPilotCoreError = $_.Exception.Message
        }
    } else {
        $script:CmdPilotCoreError = "缺少预测器核心源码: $script:CmdPilotCoreSource"
    }
    $script:CmdPilotCoreType = Get-CmdPilotCoreType
}

function New-CmdPilotPredictor {
    <#
    .SYNOPSIS
    构造编译实现的预测器（伴侣进程路径来自模块作用域）。
    .OUTPUTS
    预测器实例；核心不可用（编译失败/文件缺失）时返回 $null——调用方据此退回
    Tab 补全，模块仍可用而不报错。
    #>
    [CmdletBinding()]
    param()
    if (-not $script:CmdPilotCoreType) { return $null }
    try {
        return $script:CmdPilotCoreType::new($script:CmdPilotCompanion)
    } catch {
        $script:CmdPilotCoreError = $_.Exception.Message
        return $null
    }
}

# ---- ③ 后台 worker：debounce + 伴侣调用，全在专用 runspace -----------------
# worker 的脚本体（模块作用域变量，Start-CmdPilotWorker 把它交给新 runspace）。
# 它只通过**传进来的预测器对象的实例方法**读写共享状态：新 runspace 里不保证能
# 解析 Add-Type 出来的类型名，而方法绑定按对象类型走，不依赖类型名解析。
$script:CmdPilotWorkerScript = {
    param($pred, $companion)
    # 已经为哪一代输入取过结果。写完快照并不改变 gen/pending，若无此标记，下一轮
    # 循环会判定"输入没变"从而对**同一个输入**再次起进程，形成永久空转重取——实测
    # 空闲 6 秒仍触发 18 次 companion 进程创建（每次约 22ms 进程创建 + 两个临时文件
    # + Defender 扫描），是窗口卡顿/输入延迟的根源之一。每代只取一次。
    $lastGen = -1
    while ($pred.WorkerAlive()) {
        Start-Sleep -Milliseconds 150
        if (-not $pred.WorkerAlive()) { break }
        $gen = $pred.WorkerGen()
        $input = $pred.WorkerPending()
        if ([string]::IsNullOrEmpty($input)) { continue }
        if ($gen -eq $lastGen) { continue }   # 本代已取过：不再起进程
        Start-Sleep -Milliseconds 150         # debounce：确认输入稳定
        if (-not $pred.WorkerIsCurrent($gen)) { continue }
        # 取之前先记账：本代即使取失败（守护进程不可用/超时）也不再重试，避免失败时
        # 形成重试风暴；下一次按键会产生新 gen，届时自然重试。
        $lastGen = $gen

        # inline 伴侣调用（纯脚本，不依赖模块函数/类方法）
        $req = @{ input = $input; shell = 'ps'; cwd = (Get-Location).Path; history = @(); trigger = 'auto' }
        $json = $req | ConvertTo-Json -Compress -Depth 5
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.json')
        $out = Join-Path ([System.IO.Path]::GetTempPath()) ('cmdpilot-w-' + [guid]::NewGuid().ToString('N') + '.out.json')
        try {
            [System.IO.File]::WriteAllText($tmp, $json)
            # 不用 ArgumentList（PS 5.1 缺失该属性）：手工拼参数串并逐参加引号
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
                    # JSON 解析留在 PowerShell 侧（核心不引 System.Text.Json），只把
                    # 字段交给核心：top 是首选建议，list 是候选列表。
                    $topText = ''
                    $topFull = ''
                    $fulls = @()
                    $sources = @()
                    if ($obj.top) {
                        $topText = [string]$obj.top.text
                        $topFull = [string]$obj.top.full
                    }
                    foreach ($it in @($obj.list)) {
                        if ($it) {
                            $fulls += [string]$it.full
                            $sources += [string]$it.source
                        }
                    }
                    $pred.WorkerPublish($gen, $input, $topText, $topFull, [string[]]$fulls, [string[]]$sources)
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

function Start-CmdPilotWorker {
    <#
    .SYNOPSIS
    启动后台取建议的 worker（幂等）。在 Enable-CmdPilot 时即启动：worker 的 runspace
    不阻塞进程退出（实测 import-only 577ms / 带 worker 653ms），提前启动可让首个建议
    不必额外等一次 runspace 创建。
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)] $Predictor)
    if ($script:CmdPilotWorkerPS) { return }
    # 专用 runspace：PowerShell scriptblock 不能在 raw .NET 线程上执行。
    $rs = [runspacefactory]::CreateRunspace()
    $rs.Open()
    $ps = [powershell]::Create()
    $ps.Runspace = $rs
    $handle = $ps.AddScript($script:CmdPilotWorkerScript).
        AddArgument($Predictor).
        AddArgument($script:CmdPilotCompanion).
        BeginInvoke()
    $script:CmdPilotWorkerRS = $rs
    $script:CmdPilotWorkerPS = $ps
    $script:CmdPilotWorkerHandle = $handle
}

function Stop-CmdPilotWorker {
    <#
    .SYNOPSIS
    停止 worker 并释放其 runspace（Disable-CmdPilot / 模块卸载用）。
    #>
    [CmdletBinding()]
    param()
    # 先置运行标志：worker 循环下一轮（≤150ms）自行退出，再 Stop() 兜底。
    if ($script:CmdPilotPredictor) {
        try { $script:CmdPilotPredictor.StopWorker() } catch { }
    }
    if ($script:CmdPilotWorkerPS) {
        try { $script:CmdPilotWorkerPS.Stop() } catch { }
        # 等 worker 真正结束再 Dispose。BeginInvoke 返回的是 PowerShellAsyncResult，
        # 它**没有** WaitOne 方法（旧实现直接 .WaitOne 会抛，被 catch 吃掉后仍在宿主
        # $Error 里留一条红字）；等的是 IAsyncResult.AsyncWaitHandle。
        if ($script:CmdPilotWorkerHandle) {
            try { $null = $script:CmdPilotWorkerHandle.AsyncWaitHandle.WaitOne(500) } catch { }
        }
        try { $script:CmdPilotWorkerPS.Dispose() } catch { }
        $script:CmdPilotWorkerPS = $null
    }
    if ($script:CmdPilotWorkerRS) {
        try { $script:CmdPilotWorkerRS.Close() } catch { }
        try { $script:CmdPilotWorkerRS.Dispose() } catch { }
        $script:CmdPilotWorkerRS = $null
    }
    $script:CmdPilotWorkerHandle = $null
}

$script:CmdPilotWorkerRS = $null
$script:CmdPilotWorkerPS = $null
$script:CmdPilotWorkerHandle = $null

# 我们的 Predictor 是否已注册在引擎里（按 Id 比对）。
# 用途：把"幂等重注册"的第一步（先注销同名 Id 再注册）从异常路径变成正常判断——
# SubsystemManager::UnregisterSubsystem 对**未注册**的 Id 会抛
#     "No implementation was registered for the subsystem 'CommandPredictor'."
# 异常即使被 catch 也会在宿主 $Error 里留一条记录（用户敲 $Error 看到红字），而
# GetSubsystemInfo 只是查询、不抛。2026-09-16 实测：SubsystemInfo.Implementations
# 元素类型为 SubsystemInfo+ImplementationInfo，属性 Id/Kind/Name/Description/ImplementationType。
function Test-CmdPilotPredictorRegistered {
    [CmdletBinding()]
    param([Parameter(Mandatory)][guid] $Id)
    $info = [System.Management.Automation.Subsystem.SubsystemManager]::GetSubsystemInfo(
        [System.Management.Automation.Subsystem.SubsystemKind]::CommandPredictor)
    foreach ($impl in $info.Implementations) {
        if ([guid]$impl.Id -eq $Id) { return $true }
    }
    return $false
}
