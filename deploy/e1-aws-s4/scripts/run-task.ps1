[CmdletBinding()]
param([ValidateSet("calibration", "retained")][string]$RunMode = "calibration")

. (Join-Path $PSScriptRoot "common.ps1")
. (Join-Path $PSScriptRoot "retained-contract.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtime = Read-RuntimeConfiguration -ModuleRoot $moduleRoot
$deadline = Assert-BeforeDeadline -RuntimeConfiguration $runtime
Assert-CommandAvailable -Name "aws"

Push-Location $moduleRoot
try {
    $configurationJson = (& terraform output -json run_task_configuration) -join [Environment]::NewLine
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($configurationJson)) {
        throw "Unable to read the applied runner configuration."
    }
}
finally {
    Pop-Location
}
$configuration = $configurationJson | ConvertFrom-Json
if ($null -eq $configuration) {
    throw "The full environment has not been applied."
}
if ($configuration.run_mode -ne $RunMode -or $configuration.run_id -ne "$RunMode-$($runtime.deployment_id)") { throw "Requested run mode does not match the applied task definition." }
if (($deadline - [DateTimeOffset]::UtcNow).TotalMinutes -lt 15) { throw "At least fifteen minutes must remain for measurement and cleanup." }
$recoveryRoot = Join-Path $moduleRoot "recovered-results\$($runtime.deployment_id)"
if (Test-Path -LiteralPath (Join-Path $recoveryRoot $configuration.run_id)) { throw "Local run results already exist; refusing to overwrite." }
if ($RunMode -eq 'retained') {
    Assert-RetainedDiagnostic -Directory (Join-Path $recoveryRoot 'resource-diagnostic') -DeploymentId $runtime.deployment_id -ImageDigest $configuration.media_digest
}
$stored = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('s3api','list-objects-v2','--bucket',$configuration.result_bucket,'--prefix',"$($configuration.result_prefix)/$($configuration.run_id)/",'--max-keys','1')
if ([int]$stored.KeyCount -ne 0) { throw "Remote run results or reservation already exist; refusing to overwrite." }

$subnets = @($configuration.subnets) -join ","
$network = "awsvpcConfiguration={subnets=[$subnets],securityGroups=[$($configuration.security_group)],assignPublicIp=ENABLED}"
$existing = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @(
    "ecs", "list-tasks",
    "--cluster", [string]$configuration.cluster,
    "--family", [string]$configuration.runner_family,
    "--desired-status", "RUNNING"
)
if (@($existing.taskArns).Count -gt 0) {
    throw "A load-generator task is already pending or running for this deployment."
}
$run = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @(
    "ecs", "run-task",
    "--cluster", [string]$configuration.cluster,
    "--task-definition", [string]$configuration.task_definition,
    "--launch-type", "FARGATE",
    "--network-configuration", $network,
    "--started-by", "e1-s4-$($runtime.deployment_id)",
    "--enable-ecs-managed-tags",
    "--propagate-tags", "TASK_DEFINITION",
    "--count", "1"
)
if (@($run.failures).Count -gt 0 -or @($run.tasks).Count -ne 1) {
    throw "ECS did not start exactly one load-generator task."
}
$taskArn = [string]$run.tasks[0].taskArn

$task = Wait-EcsTaskStopped `
    -Profile $runtime.aws_profile `
    -Region $runtime.region `
    -Cluster $configuration.cluster `
    -TaskArn $taskArn `
    -StopAt $deadline
$container = @($task.containers | Where-Object name -eq "load-generator") | Select-Object -First 1
if ($null -eq $container -or $null -eq $container.exitCode) {
    throw "The stopped load-generator task did not report an exit code."
}

$recoveryRoot = Join-Path $moduleRoot "recovered-results\$($runtime.deployment_id)"
New-Item -ItemType Directory -Path $recoveryRoot -Force | Out-Null
Invoke-CheckedCommand -FilePath "aws" -Arguments @(
    "s3", "sync",
    "s3://$($configuration.result_bucket)/$($configuration.result_prefix)/",
    $recoveryRoot,
    "--profile", [string]$runtime.aws_profile,
    "--region", [string]$runtime.region,
    "--no-cli-pager",
    "--only-show-errors"
) | Out-Null

$recoveredFiles = @(Get-ChildItem -LiteralPath $recoveryRoot -File -Recurse)
if ([int]$container.exitCode -ne 0) {
    throw "The load-generator task exited with code $($container.exitCode); result sync completed and $($recoveredFiles.Count) local files are available. Check runner logs for the original failure."
}
if ($recoveredFiles.Count -eq 0) {
    throw "The load-generator task exited successfully but no result files were recovered."
}
Write-Host "The load-generator task completed successfully and raw results were recovered locally."
