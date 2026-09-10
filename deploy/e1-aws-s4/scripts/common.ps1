Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($PSVersionTable.PSVersion.Major -lt 7) {
    throw "PowerShell 7 or later is required by the timed deployment workflow."
}

function Assert-CommandAvailable {
    param([Parameter(Mandatory)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' is not available on PATH."
    }
}

function Invoke-CheckedCommand {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $lines = @(& $FilePath @Arguments)
    if ($LASTEXITCODE -ne 0) {
        throw "External command '$FilePath' failed with exit code $LASTEXITCODE."
    }
    return $lines
}

function Invoke-AwsJson {
    param(
        [Parameter(Mandatory)][string]$Profile,
        [Parameter(Mandatory)][string]$Region,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $allArguments = @($Arguments) + @(
        "--profile", $Profile,
        "--region", $Region,
        "--no-cli-pager",
        "--output", "json"
    )
    $lines = Invoke-CheckedCommand -FilePath "aws" -Arguments $allArguments
    $raw = $lines -join [Environment]::NewLine
    if ([string]::IsNullOrWhiteSpace($raw)) {
        return $null
    }
    return $raw | ConvertFrom-Json
}

function Get-LiveTaggedResources {
    param(
        [AllowEmptyCollection()][object[]]$Resources,
        [Parameter(Mandatory)][string]$Profile,
        [Parameter(Mandatory)][string]$Region
    )

    # The tagging index retains deleted EC2 resources and inactive ECS records.
    # Unknown resource types remain blocking; only verified terminal records pass.
    $ec2Cache = @{}
    foreach ($resource in $Resources) {
        $arn = [string]$resource.ResourceARN
        $parts = $arn -split ':', 6
        $path = $parts[5] -split '/'
        $kind = "$($parts[2]):$($path[0])"
        $live = $true
        switch ($kind) {
            'ec2:security-group-rule' {
                if (-not $ec2Cache.ContainsKey($kind)) {
                    $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ec2', 'describe-security-group-rules')
                    $ec2Cache[$kind] = @($result.SecurityGroupRules | ForEach-Object SecurityGroupRuleId)
                }
                $live = $path[1] -in $ec2Cache[$kind]
            }
            'ec2:vpc-endpoint' {
                if (-not $ec2Cache.ContainsKey($kind)) {
                    $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ec2', 'describe-vpc-endpoints')
                    $ec2Cache[$kind] = @($result.VpcEndpoints | Where-Object State -ne 'deleted' | ForEach-Object VpcEndpointId)
                }
                $live = $path[1] -in $ec2Cache[$kind]
            }
            'ecs:cluster' {
                $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ecs', 'describe-clusters', '--clusters', $arn)
                if (@($result.failures | Where-Object reason -ne 'MISSING').Count) { throw 'Unable to verify a tagged ECS cluster.' }
                $live = @($result.clusters | Where-Object { $_.status -ne 'INACTIVE' -or $_.runningTasksCount -gt 0 -or $_.pendingTasksCount -gt 0 -or $_.activeServicesCount -gt 0 }).Count -gt 0
            }
            'ecs:service' {
                $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ecs', 'describe-services', '--cluster', $path[1], '--services', $arn)
                if (@($result.failures | Where-Object reason -ne 'MISSING').Count) { throw 'Unable to verify a tagged ECS service.' }
                $live = @($result.services | Where-Object { $_.status -ne 'INACTIVE' -or $_.runningCount -gt 0 -or $_.pendingCount -gt 0 }).Count -gt 0
            }
            'ecs:task' {
                $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ecs', 'describe-tasks', '--cluster', $path[1], '--tasks', $arn)
                if (@($result.failures | Where-Object reason -ne 'MISSING').Count) { throw 'Unable to verify a tagged ECS task.' }
                $live = @($result.tasks | Where-Object lastStatus -ne 'STOPPED').Count -gt 0
            }
            'ecs:task-definition' {
                $result = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @('ecs', 'describe-task-definition', '--task-definition', $arn)
                $live = $result.taskDefinition.status -notin @('INACTIVE', 'DELETE_IN_PROGRESS')
            }
        }
        if ($live) { $resource }
    }
}

function Invoke-Terraform {
    param(
        [Parameter(Mandatory)][string]$ModuleRoot,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    Push-Location $ModuleRoot
    try {
        & terraform @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Terraform failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        Pop-Location
    }
}

function Read-RuntimeConfiguration {
    param([Parameter(Mandatory)][string]$ModuleRoot)

    $path = Join-Path $ModuleRoot "runtime.auto.tfvars.json"
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Missing runtime.auto.tfvars.json. Run bootstrap-images.ps1 first."
    }
    return Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
}

function Assert-BeforeDeadline {
    param([Parameter(Mandatory)][object]$RuntimeConfiguration)

    $deadline = [DateTimeOffset]::Parse($RuntimeConfiguration.expires_at).ToUniversalTime()
    if ([DateTimeOffset]::UtcNow -ge $deadline) {
        throw "The configured deployment deadline has passed. Run destroy.ps1 immediately."
    }
    return $deadline
}

function Wait-EcsTaskStopped {
    param(
        [Parameter(Mandatory)][string]$Profile,
        [Parameter(Mandatory)][string]$Region,
        [Parameter(Mandatory)][string]$Cluster,
        [Parameter(Mandatory)][string]$TaskArn,
        [Parameter(Mandatory)][DateTimeOffset]$StopAt
    )

    while ([DateTimeOffset]::UtcNow -lt $StopAt) {
        $description = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
            "ecs", "describe-tasks",
            "--cluster", $Cluster,
            "--tasks", $TaskArn
        )
        if (@($description.failures).Count -gt 0 -or @($description.tasks).Count -ne 1) {
            throw "ECS could not describe the one-shot load-generator task."
        }
        if ($description.tasks[0].lastStatus -eq "STOPPED") {
            return $description.tasks[0]
        }
        $remainingSeconds = [Math]::Floor(($StopAt - [DateTimeOffset]::UtcNow).TotalSeconds)
        if ($remainingSeconds -gt 0) {
            Start-Sleep -Seconds ([Math]::Min(5, $remainingSeconds))
        }
    }
    throw "The load-generator task did not stop before the mandatory deployment deadline."
}
