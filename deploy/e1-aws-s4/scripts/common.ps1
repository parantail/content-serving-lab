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
        throw "The two-hour deployment deadline has passed. Run destroy.ps1 immediately."
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
