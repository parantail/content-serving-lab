# Reuse checked CLI calls and the established local watchdog helpers.
. (Join-Path $PSScriptRoot '../../e1-aws-s4/scripts/common.ps1')
$script:E2Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

function Get-E2Runtime {
    Read-RuntimeConfiguration -ModuleRoot $script:E2Root
}
function Get-E2Configuration {
    $raw = Invoke-CheckedCommand terraform @("-chdir=$script:E2Root", 'output', '-json', 'run_configuration')
    ($raw -join "`n") | ConvertFrom-Json
}
function Save-E2Json {
    param([string]$Path, [object]$Value)
    $Value | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath $Path -Encoding utf8NoBOM
}
function Receive-E2Results {
    param([object]$Runtime, [object]$Configuration)
    $recovery = Join-Path $script:E2Root "local/recovered/$($Runtime.deployment_id)"
    New-Item -ItemType Directory -Path $recovery -Force | Out-Null
    Invoke-CheckedCommand aws @('s3', 'sync', "s3://$($Configuration.result_bucket)/", $recovery,
        '--profile', $Runtime.aws_profile, '--region', $Runtime.region, '--only-show-errors') | Out-Null
    return $recovery
}
