#Requires -Version 7.4
# PredictorNew.ps1 - PS 7.4+ / PSReadLine 2.3.4+ 引擎级 Subsystem API 实现。
# 仅由 CmdPilot.psm1 探测到 System.Management.Automation.Subsystem.Prediction
# 存在时 dot-source（解析时机在探测之后，避免旧主机解析失败）。
using namespace System.Management.Automation.Subsystem.Prediction

class CmdPilotPredictor : CmdPilotPredictorBase, System.Management.Automation.Subsystem.Prediction.ICommandPredictor {
    # ISubsystem 成员（Id 为 Guid；引擎用 Name/Description 做注册信息）。
    [guid] $Id = [guid]'7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f'
    [string] $Name = 'CmdPilot'
    [string] $Description = 'CmdPilot 智能补全助手 (本地知识库 + OpenAI 兼容 AI + 频率推荐)'
    [System.Collections.Generic.Dictionary[string,string]] $FunctionsToDefine = [System.Collections.Generic.Dictionary[string,string]]::new()

    # 建议入口：读快照 + 登记输入，绝不阻塞。
    [SuggestionPackage] GetSuggestion([PredictionClient] $client, [PredictionContext] $context, [System.Threading.CancellationToken] $ct) {
        # SuggestionPackage 是值类型，不能返回 null，且其构造器拒绝空/空串条目：
        # "无建议"返回含一个零宽空格条目的包（渲染完全不可见）。
        $nosug = [System.Collections.Generic.List[PredictiveSuggestion]]::new()
        $nosug.Add([PredictiveSuggestion]::new([string][char]0x200B))
        if ($ct.IsCancellationRequested) { return [SuggestionPackage]::new($nosug) }
        if ($null -eq $context -or $null -eq $context.InputAst) { return [SuggestionPackage]::new($nosug) }
        $input = $context.InputAst.Extent.Text
        if ([string]::IsNullOrWhiteSpace($input)) { return [SuggestionPackage]::new($nosug) }
        $snap = $this.RegisterAndGetSnapshot($input)
        if ($this.GetTabOnly() -or -not $snap.current) { return [SuggestionPackage]::new($nosug) }

        $entries = [System.Collections.Generic.List[PredictiveSuggestion]]::new()
        if ($snap.top -and $snap.top.text) {
            $entries.Add([PredictiveSuggestion]::new([string]$snap.top.text))
        }
        foreach ($s in $snap.list) {
            $full = [string]$s.full
            if ([string]::IsNullOrEmpty($full)) { continue }
            $tooltip = '{0}  [{1}]' -f $full, $s.source
            $entries.Add([PredictiveSuggestion]::new($full, $tooltip))
        }
        if ($entries.Count -eq 0) { return [SuggestionPackage]::new($nosug) }
        return [SuggestionPackage]::new($entries)
    }

    [bool] CanAcceptFeedback([PredictionClient] $client, [PredictorFeedbackKind] $feedback) { return $true }

    [void] OnSuggestionDisplayed([PredictionClient] $client, [uint32] $session, [int] $countOrIndex) { }

    # 接受建议即视为执行一次（M5 统计）。
    [void] OnSuggestionAccepted([PredictionClient] $client, [uint32] $session, [string] $acceptedSuggestion) {
        $full = $null
        # 基类状态统一放在 $this.Sync 哈希表（无 Lock/SnapshotFull 成员）。
        [System.Threading.Monitor]::Enter($this.Sync.lock)
        try {
            if ($this.Sync.snapFull) { $full = $this.Sync.snapFull }
        } finally {
            [System.Threading.Monitor]::Exit($this.Sync.lock)
        }
        if (-not $full) { $full = $acceptedSuggestion }
        $dir = ''
        if ($client -and $client.CurrentLocation) { $dir = [string]$client.CurrentLocation.Path }
        Send-CmdPilotReport -Command $full -Dir $dir -Shell 'ps'
    }

    [void] OnCommandLineAccepted([PredictionClient] $client, [System.Collections.Generic.IReadOnlyList[string]] $history) { }

    # 每条命令执行后记录（M5 统计，覆盖手输命令）。
    [void] OnCommandLineExecuted([PredictionClient] $client, [string] $commandLine, [bool] $success) {
        if ([string]::IsNullOrWhiteSpace($commandLine)) { return }
        $dir = ''
        if ($client -and $client.CurrentLocation) { $dir = [string]$client.CurrentLocation.Path }
        Send-CmdPilotReport -Command $commandLine -Dir $dir -Shell 'ps'
    }

    [void] Dispose() { $this.StopWorker() }
}
