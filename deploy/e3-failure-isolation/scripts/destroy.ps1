[CmdletBinding()]
param([switch]$FromWatchdog)
. (Join-Path $PSScriptRoot 'common.ps1')
$runtime = Get-E3Runtime
$name = "content-serving-e3-$($runtime.deployment_id)"
$cleanupLog = Join-Path $script:E3Root 'local/cleanup.json'
$cleanup = [ordered]@{started=[DateTimeOffset]::UtcNow.ToString('o'); stopped_tasks=0; recovered=$false}
# Find the dedicated cluster by its deterministic name, including partial apply.
$clusters = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','describe-clusters','--clusters',$name)
if (@($clusters.failures | Where-Object reason -ne 'MISSING').Count) { throw 'Cannot resolve E3 cluster for cleanup.' }
$cluster = @($clusters.clusters | Where-Object status -eq 'ACTIVE') | Select-Object -First 1
if ($null -ne $cluster) {
    $tasks = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','list-tasks','--cluster',$cluster.clusterArn)
    foreach ($task in @($tasks.taskArns)) {
        Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','stop-task','--cluster',$cluster.clusterArn,'--task',$task,'--reason','E3 mandatory cleanup') | Out-Null
        $cleanup.stopped_tasks++
    }
    foreach ($task in @($tasks.taskArns)) {
        Wait-EcsTaskStopped $runtime.aws_profile $runtime.region $cluster.clusterArn $task ([DateTimeOffset]::UtcNow.AddMinutes(1)) | Out-Null
    }
}
$buckets = Invoke-AwsJson $runtime.aws_profile $runtime.region @('s3api','list-buckets')
if (@($buckets.Buckets | Where-Object Name -eq $name).Count) {
    try {
        Receive-E3Results $runtime ([pscustomobject]@{result_bucket=$name}) | Out-Host
        $cleanup.recovered=$true
    } catch {
        # A recovery error must not extend the mandatory infrastructure deadline.
        $cleanup['recovery_error']=$_.Exception.Message
        Write-Warning 'Result recovery failed; continuing mandatory destroy. Previously uploaded/local raw is preserved.'
    }
}
Save-E3Json $cleanupLog $cleanup
Invoke-Terraform $script:E3Root @('destroy','-input=false','-auto-approve')
& (Join-Path $PSScriptRoot 'verify-destroy.ps1') -DeploymentId $runtime.deployment_id -Profile $runtime.aws_profile
$cleanup['ended']=[DateTimeOffset]::UtcNow.ToString('o')
$cleanup['verified']=$true
Save-E3Json $cleanupLog $cleanup
if (-not $FromWatchdog) {
    $pidPath = Join-Path $script:E3Root 'watchdog.pid'
    if (Test-Path -LiteralPath $pidPath) {
        $watchdogPid = [int](Get-Content -LiteralPath $pidPath -Raw)
        $process = Get-CimInstance Win32_Process -Filter "ProcessId = $watchdogPid" -ErrorAction SilentlyContinue
        $expectedPath = Join-Path $PSScriptRoot 'watchdog.ps1'
        if ($null -ne $process -and $process.CommandLine.Contains($expectedPath)) { Stop-Process -Id $watchdogPid -Force }
    }
}
# Keep the runtime/deadline and all logs for review in ignored paths. A new
# deployment requires explicitly archiving these records after verified cleanup.
Write-Host 'E3 resources destroyed and independently checked.'
