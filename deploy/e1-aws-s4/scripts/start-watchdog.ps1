[CmdletBinding()]
param()

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$deadlinePath = Join-Path $moduleRoot "deadline.json"
$pidPath = Join-Path $moduleRoot "watchdog.pid"

if (-not (Test-Path -LiteralPath $deadlinePath -PathType Leaf)) {
    throw "Missing deadline.json."
}

if (Test-Path -LiteralPath $pidPath -PathType Leaf) {
    $recordedPid = 0
    if ([int]::TryParse((Get-Content -LiteralPath $pidPath -Raw).Trim(), [ref]$recordedPid)) {
        $existingWatchdog = Get-CimInstance Win32_Process -Filter "ProcessId = $recordedPid" -ErrorAction SilentlyContinue
        if ($null -ne $existingWatchdog -and $existingWatchdog.CommandLine -like "*watchdog.ps1*") {
            Write-Host "Destruction watchdog is already running."
            return
        }
    }
}

$powershell = (Get-Process -Id $PID -ErrorAction Stop).Path
$watchdogPath = Join-Path $PSScriptRoot "watchdog.ps1"
$process = Start-Process `
    -FilePath $powershell `
    -ArgumentList @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$watchdogPath`"") `
    -WindowStyle Hidden `
    -PassThru
$process.Id | Set-Content -LiteralPath $pidPath -Encoding ascii
Write-Host "Mandatory destruction watchdog started."
