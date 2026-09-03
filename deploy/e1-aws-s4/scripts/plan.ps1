[CmdletBinding()]
param()

. (Join-Path $PSScriptRoot "common.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtime = Read-RuntimeConfiguration -ModuleRoot $moduleRoot
$deadline = Assert-BeforeDeadline -RuntimeConfiguration $runtime

& (Join-Path $PSScriptRoot "preflight.ps1") `
    -DeploymentId $runtime.deployment_id `
    -ExpectedCostUsd $runtime.expected_cost_usd `
    -Profile $runtime.aws_profile `
    -Region $runtime.region `
    -ExpectedTaggedResources 2

Invoke-Terraform -ModuleRoot $moduleRoot -Arguments @("init", "-input=false", "-lockfile=readonly")
Invoke-Terraform -ModuleRoot $moduleRoot -Arguments @("validate")

$planPath = Join-Path $moduleRoot "e1-aws-s4.tfplan"
Push-Location $moduleRoot
try {
    & terraform plan -input=false -out=$planPath -detailed-exitcode
    $planExitCode = $LASTEXITCODE
    if ($planExitCode -eq 0) {
        throw "The full environment plan contains no changes. Refusing a stale or already-applied workflow."
    }
    if ($planExitCode -ne 2) {
        throw "Terraform plan failed with exit code $planExitCode."
    }
    $planJson = (& terraform show -json $planPath) -join [Environment]::NewLine
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to inspect the saved Terraform plan."
    }
}
finally {
    Pop-Location
}

$plan = $planJson | ConvertFrom-Json -Depth 100
$managedChanges = @($plan.resource_changes | Where-Object mode -eq "managed")
$creates = @($managedChanges | Where-Object { ($_.change.actions -join ",") -eq "create" })
$unsafeChanges = @($managedChanges | Where-Object {
    $actions = @($_.change.actions)
    "delete" -in $actions -or "update" -in $actions
})
if ($unsafeChanges.Count -gt 0) {
    throw "The saved plan contains update, replacement, or deletion actions. Start from a clean bootstrap state."
}
if ($creates.Count -ne 67) {
    throw "Expected exactly 67 full-environment creates after ECR bootstrap; found $($creates.Count)."
}
$forbiddenTypes = @(
    "aws_nat_gateway",
    "aws_eip",
    "aws_cloudfront_distribution",
    "aws_wafv2_web_acl",
    "aws_appautoscaling_target",
    "aws_appautoscaling_policy"
)
$presentForbiddenTypes = @($creates | Where-Object type -in $forbiddenTypes)
if ($presentForbiddenTypes.Count -gt 0) {
    throw "The plan includes a forbidden cost or autoscaling resource type."
}

$remaining = $deadline - [DateTimeOffset]::UtcNow
Write-Host "Terraform plan verified: 67 creates, 0 updates/replacements/deletes, no forbidden resource types."
Write-Host "Reviewed maximum expected cost: US`$$($runtime.expected_cost_usd)"
Write-Host "Time remaining before mandatory destruction: $([Math]::Floor($remaining.TotalMinutes)) minutes"
Write-Host "Next: run .\scripts\apply.ps1"
