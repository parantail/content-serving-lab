[CmdletBinding()]
param(
    [switch]$FromWatchdog
)

. (Join-Path $PSScriptRoot "common.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtime = Read-RuntimeConfiguration -ModuleRoot $moduleRoot
Assert-CommandAvailable -Name "aws"
Assert-CommandAvailable -Name "terraform"

if ($runtime.enable_environment) {
    Push-Location $moduleRoot
    try {
        $configurationJson = (& terraform output -json run_task_configuration 2>$null) -join [Environment]::NewLine
        $configurationExitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    if ($configurationExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($configurationJson) -and $configurationJson -ne "null") {
        $configuration = $configurationJson | ConvertFrom-Json
        $runningTasks = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @(
            "ecs", "list-tasks",
            "--cluster", [string]$configuration.cluster,
            "--family", [string]$configuration.runner_family,
            "--desired-status", "RUNNING"
        )
        $taskArns = @($runningTasks.taskArns)
        foreach ($taskArn in $taskArns) {
            Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @(
                "ecs", "stop-task",
                "--cluster", [string]$configuration.cluster,
                "--task", [string]$taskArn,
                "--reason", "E1 AWS S4 mandatory cleanup"
            ) | Out-Null
        }
        if ($taskArns.Count -gt 0) {
            $stopWaitDeadline = [DateTimeOffset]::UtcNow.AddMinutes(1)
            $allStopped = $false
            while ([DateTimeOffset]::UtcNow -lt $stopWaitDeadline) {
                $descriptions = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments (@(
                    "ecs", "describe-tasks",
                    "--cluster", [string]$configuration.cluster,
                    "--tasks"
                ) + @($taskArns))
                $allStopped = @($descriptions.tasks | Where-Object lastStatus -ne "STOPPED").Count -eq 0
                if ($allStopped) { break }
                Start-Sleep -Seconds 5
            }
            if ($allStopped) {
                Write-Host "Running one-shot load-generator tasks were stopped at cleanup."
            }
            else {
                Write-Warning "A stopped state was not observed within one minute; cleanup will continue."
            }
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
        Write-Host "Available raw results were recovered before destroy."
    }
}

Invoke-Terraform -ModuleRoot $moduleRoot -Arguments @("destroy", "-input=false", "-auto-approve")
& (Join-Path $PSScriptRoot "verify-destroy.ps1") `
    -DeploymentId $runtime.deployment_id `
    -Profile $runtime.aws_profile `
    -Region $runtime.region

$watchdogPidPath = Join-Path $moduleRoot "watchdog.pid"
if (-not $FromWatchdog -and (Test-Path -LiteralPath $watchdogPidPath -PathType Leaf)) {
    $watchdogPid = 0
    if ([int]::TryParse((Get-Content -LiteralPath $watchdogPidPath -Raw).Trim(), [ref]$watchdogPid)) {
        $watchdogProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $watchdogPid" -ErrorAction SilentlyContinue
        if ($null -ne $watchdogProcess -and $watchdogProcess.CommandLine -like "*watchdog.ps1*") {
            Stop-Process -Id $watchdogPid -Force
        }
    }
}

$generatedPaths = @(
    (Join-Path $moduleRoot "runtime.auto.tfvars.json"),
    (Join-Path $moduleRoot "deadline.json"),
    (Join-Path $moduleRoot "e1-aws-s4.tfplan"),
    (Join-Path $moduleRoot "watchdog.pid")
)
foreach ($path in $generatedPaths) {
    if (Test-Path -LiteralPath $path) {
        Remove-Item -LiteralPath $path -Force
    }
}

if ($FromWatchdog) {
    Write-Host "Mandatory deadline cleanup completed."
}
else {
    Write-Host "Result recovery, Terraform destroy, and independent residual-resource checks completed."
}
