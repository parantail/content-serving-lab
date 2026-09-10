Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$deadlinePath = Join-Path $moduleRoot "deadline.json"
$logPath = Join-Path $moduleRoot "watchdog.log"

try {
    $metadata = Get-Content -LiteralPath $deadlinePath -Raw | ConvertFrom-Json
    $deadline = [DateTimeOffset]::Parse($metadata.expires_at).ToUniversalTime()
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $seconds = [Math]::Ceiling(($deadline - [DateTimeOffset]::UtcNow).TotalSeconds)
        Start-Sleep -Seconds ([Math]::Max(1, [Math]::Min(30, $seconds)))
    }
    "[$([DateTimeOffset]::UtcNow.ToString('o'))] Deadline reached; starting mandatory result recovery and destroy." |
        Set-Content -LiteralPath $logPath -Encoding utf8NoBOM
    & (Join-Path $PSScriptRoot "destroy.ps1") -FromWatchdog *>> $logPath
}
catch {
    "[$([DateTimeOffset]::UtcNow.ToString('o'))] Watchdog failed: $($_.Exception.Message)" |
        Add-Content -LiteralPath $logPath -Encoding utf8NoBOM
    exit 1
}
