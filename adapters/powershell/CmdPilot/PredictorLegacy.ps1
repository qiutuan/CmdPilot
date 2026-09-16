#Requires -Version 5.1
# PredictorLegacy.ps1 - 经典 PSReadLine ICommandPredictor（PSReadLine 2.2.x，
# PS 5.1 / PS 7.0-7.3）实现。仅由 CmdPilot.psm1 在探测不到引擎级 Subsystem
# API 时 dot-source（避免在 PS 7.4+ 主机解析失败）。
using namespace Microsoft.PowerShell.PSReadLine

class CmdPilotPredictor : CmdPilotPredictorBase, Microsoft.PowerShell.PSReadLine.ICommandPredictor {
    [string] $Id = 'CmdPilot.Predictor'
    [bool] $CanAcceptFeedback = $true

    # 建议入口：读快照 + 登记输入，绝不阻塞。inline + list 双视图。
    [PredictionResult] GetSuggestion([PredictionContext] $context, [System.Threading.CancellationToken] $ct) {
        if ($ct.IsCancellationRequested) { return $null }
        if ($null -eq $context) { return $null }
        $input = $context.InputLine
        if ([string]::IsNullOrWhiteSpace($input)) { return $null }
        $snap = $this.RegisterAndGetSnapshot($input)
        if ($this.GetTabOnly() -or -not $snap.current) { return $null }

        $inlineText = ''
        if ($snap.top -and $snap.top.text) { $inlineText = [string]$snap.top.text }
        $items = [System.Collections.Generic.List[ListPredictionItem]]::new()
        foreach ($s in $snap.list) {
            $full = [string]$s.full
            if ([string]::IsNullOrEmpty($full)) { continue }
            $label = '{0}  [{1}]' -f $full, $s.source
            $items.Add([ListPredictionItem]::new($full, $label))
        }
        if ($items.Count -eq 0) { return $null }
        $listPred = [ListPrediction]::new($items.ToArray())
        $inlinePred = $null
        if (-not [string]::IsNullOrEmpty($inlineText)) {
            $inlinePred = [InlinePrediction]::new($inlineText)
        }
        return [PredictionResult]::new($inlinePred, $listPred)
    }

    # 接受建议即视为执行一次（M5 统计）。
    [void] AcceptSuggestion([PredictionContext] $context, [string] $suggestion) {
        $full = $null
        [System.Threading.Monitor]::Enter($this.Lock)
        try {
            if ($this.SnapshotFull) { $full = $this.SnapshotFull }
        } finally {
            [System.Threading.Monitor]::Exit($this.Lock)
        }
        if (-not $full) { $full = $context.InputLine + $suggestion }
        Send-CmdPilotReport -Command $full -Dir (Get-Location).Path -Shell 'ps'
    }

    [void] IgnoreSuggestion([PredictionContext] $context, [string] $suggestion) { }

    [void] Start() { $this.StartWorker() }

    [void] End() { $this.StopWorker() }
}
