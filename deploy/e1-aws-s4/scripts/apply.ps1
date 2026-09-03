[CmdletBinding()]
param()

. (Join-Path $PSScriptRoot "common.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtime = Read-RuntimeConfiguration -ModuleRoot $moduleRoot
$deadline = Assert-BeforeDeadline -RuntimeConfiguration $runtime
$planPath = Join-Path $moduleRoot "e1-aws-s4.tfplan"
if (-not (Test-Path -LiteralPath $planPath -PathType Leaf)) {
    throw "Missing saved plan. Run plan.ps1 first."
}

& (Join-Path $PSScriptRoot "start-watchdog.ps1")
Invoke-Terraform -ModuleRoot $moduleRoot -Arguments @("apply", "-input=false", $planPath)

$remaining = $deadline - [DateTimeOffset]::UtcNow
Write-Host "The E1 AWS S4 environment is applied."
Write-Host "Mandatory destruction remains scheduled for $($deadline.ToString('o'))."
Write-Host "Time remaining: $([Math]::Floor($remaining.TotalMinutes)) minutes"
Write-Host "Next: run .\scripts\run-task.ps1 for the A5 calibration."
