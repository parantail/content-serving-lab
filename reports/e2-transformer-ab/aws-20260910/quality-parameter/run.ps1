[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Image,
    [string]$RunId=('quality-parameter-'+(Get-Date -Format 'yyyyMMdd-HHmmss'))
)
$ErrorActionPreference='Stop'
if($RunId -notmatch '^[a-zA-Z0-9-]+$'){throw 'Invalid run ID.'}
$repo=(Resolve-Path (Join-Path $PSScriptRoot '../../../..')).Path
$output=Join-Path $repo "dist/$RunId"
New-Item -ItemType Directory -Path $output -ErrorAction Stop|Out-Null
docker run --rm --network none --mount "type=bind,source=$repo,target=/src" -w /src e2-dev:local gcc -shared -fPIC -o "dist/$RunId/codec_trace.so" reports/e2-transformer-ab/aws-20260910/quality-parameter/codec_trace.c -lheif -ldl
if($LASTEXITCODE -ne 0){throw 'Probe compilation failed.'}
$job=@{id='quality-parameter';root='/app/corpus';output='/results/unused';spec=@{operation='cover';format='avif';quality=80};concurrency=1;repetition=0;seed=209;quality=$true;save=$false;warmup=$false}|ConvertTo-Json
foreach($engine in @('magick','vips')){
    $job|docker run --rm -i --network none --cpus=1 --memory=2g --env LD_PRELOAD=/probe/codec_trace.so --mount "type=bind,source=$output,target=/probe,readonly" --entrypoint "/app/e2-$engine" $Image > "$output/$engine.jsonl" 2> "$output/$engine.log"
    if($LASTEXITCODE -ne 0){throw "Diagnostic worker failed: $engine"}
    $observations=@(Get-Content "$output/$engine.log"|Where-Object {$_ -match '^E2_QUALITY '})
    if($observations.Count -ne 8 -or @($observations|Where-Object {$_ -notmatch 'query_error=0$'}).Count){throw 'Missing or invalid quality observations.'}
    Write-Host "$engine`: $($observations[0]); observations=$($observations.Count)"
}
Write-Host "Local diagnostic results: dist/$RunId; do not combine timings with AWS measurements."
