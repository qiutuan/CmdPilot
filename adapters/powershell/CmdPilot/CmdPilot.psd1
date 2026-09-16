@{
    RootModule           = 'CmdPilot.psm1'
    ModuleVersion        = '0.1.0'
    GUID                 = '7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f'
    Author               = 'CmdPilot Team'
    CompanyName          = 'CmdPilot'
    Copyright            = '(c) CmdPilot. MIT License.'
    Description          = 'CmdPilot - Windows 命令行智能补全助手 (PSReadLine Predictor 适配层)。本地知识库兜底 + OpenAI 兼容 AI 增强 + 频率统计推荐。'
    PowerShellVersion    = '5.1'
    CompatiblePSEditions = @('Desktop', 'Core')
    FunctionsToExport    = @(
        'Enable-CmdPilot',
        'Disable-CmdPilot',
        'Set-CmdPilotMode',
        'Get-CmdPilotStatus',
        'Sync-CmdPilotHistory',
        'cmdpilot'
    )
    CmdletsToExport      = @()
    VariablesToExport    = @()
    AliasesToExport      = @()
    PrivateData          = @{
        PSData = @{
            Tags         = @('CmdPilot', 'Completion', 'Predictor', 'PSReadLine', 'AI')
            ProjectUri   = 'https://github.com/qiutuan/CmdPilot'
            LicenseUri   = 'https://github.com/qiutuan/CmdPilot/blob/main/LICENSE'
            ReleaseNotes = '0.1.0: PSReadLine Predictor adapter (inline ghost text + list view), async debounce, Tab mode, usage recording.'
        }
    }
}
