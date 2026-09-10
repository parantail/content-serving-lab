[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Image,
    [string]$RunId=('effective-quality-'+(Get-Date -Format 'yyyyMMdd-HHmmss'))
)
$ErrorActionPreference='Stop'
if($RunId -notmatch '^[a-zA-Z0-9-]+$'){throw 'Invalid run ID.'}
$repo=(Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$output=Join-Path $repo "dist/$RunId"
New-Item -ItemType Directory -Path $output -ErrorAction Stop|Out-Null
$checks=@()
foreach($quality in @(50,65,80,90)){
    foreach($engine in @('vips','magick')){
        $id="$engine-q$quality"
        $job=@{id=$id;root='/app/corpus';output='/results/unused';spec=@{operation='cover';format='avif';quality=$quality};concurrency=1;repetition=0;seed=209;quality=$true;save=$false;warmup=$false}|ConvertTo-Json
        $job|docker run --rm -i --network none --cpus=1 --memory=2g --env LD_PRELOAD=/app/e2-codec-trace.so --entrypoint "/app/e2-$engine" $Image > "$output/$id.jsonl" 2> "$output/$id.log"
        if($LASTEXITCODE -ne 0){throw "Diagnostic worker failed: $id"}
        $observations=@(Get-Content "$output/$id.log"|Where-Object {$_ -match '^E2_CODEC '})
        $pattern="^E2_CODEC encoder=AOMedia Project AV1 Encoder v3\.12\.1 threads=(\d+) speed=5 quality=$quality query_errors=0,0,0$"
        if($observations.Count -ne 8 -or @($observations|Where-Object {$_ -notmatch $pattern}).Count){throw "Missing/incorrect effective quality: $id"}
        $events=@(Get-Content "$output/$id.jsonl"|ForEach-Object {$_|ConvertFrom-Json})
        $finished=@($events|Where-Object {$_.type -eq 'finish'})
        if($finished.Count -ne 8 -or @($finished|Where-Object {$_.error -or $_.warmup}).Count){throw "Invalid diagnostic transforms: $id"}
        $checks+=@{engine=$engine;requested_quality=$quality;effective_quality=$quality;samples=8;query_errors=@(0,0,0)}
        Write-Host "$id`: actual quality $quality confirmed for 8 inputs."
    }
}
$record=@{schema='e2-effective-quality-sweep-v1';cohort='local';image=$Image;native_calls=64;checks=$checks;valid=$true}|ConvertTo-Json -Depth 8
[IO.File]::WriteAllText("$output/verification.json",$record.Replace("`r`n","`n")+"`n",[Text.UTF8Encoding]::new($false))
Write-Host "Local diagnostic results: dist/$RunId. Timing is excluded from performance results; use e2-run validate/calibrate for supervised timeout checks."
