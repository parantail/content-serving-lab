[CmdletBinding()]
param([string]$RunId = ('codec-' + (Get-Date -Format 'yyyyMMdd-HHmmss')))
$ErrorActionPreference = 'Stop'
if ($RunId -notmatch '^[a-zA-Z0-9-]+$') { throw 'Invalid run ID' }
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$output = Join-Path $repo "dist/$RunId"
if (Test-Path -LiteralPath $output) { throw 'Output already exists' }
New-Item -ItemType Directory -Path $output | Out-Null
$job = @{
    id = 'codec-probe'; root = '/src/experiments/e2-transformer-ab/fixtures/generated'
    output = '/src/dist/unused'; spec = @{operation='cover';format='avif';quality=80}
    concurrency = 1; repetition = 0; seed = 209; quality = $true; save = $false; warmup = $false
} | ConvertTo-Json
foreach ($engine in @('vips','magick')) {
    $job | & docker run --rm -i --network none --cpus=1 --memory=2g `
        --env OMP_NUM_THREADS=1 --env MAGICK_THREAD_LIMIT=1 `
        --env LD_PRELOAD=/src/dist/e2-codec-trace.so `
        --mount "type=bind,source=$repo,target=/src" -w /src e2-dev:local "/src/dist/e2-$engine" `
        > (Join-Path $output "$engine.jsonl") 2> (Join-Path $output "$engine.log")
    if ($LASTEXITCODE -ne 0) { throw "Worker failed: $engine; preserve raw output" }
    $lines = @(Get-Content -LiteralPath (Join-Path $output "$engine.log") | Where-Object { $_ -match '^E2_CODEC ' })
    if ($lines.Count -ne 8 -or @($lines | Where-Object {$_ -notmatch 'speed=5 quality=80 query_errors=0,0,0$'}).Count) { throw 'Missing/invalid codec observations' }
    Write-Host "$engine`: $($lines[0]); observations=$($lines.Count)"
}
Write-Host "Raw results: dist/$RunId"
