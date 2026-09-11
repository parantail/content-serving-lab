[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('calibrate','measure')][string]$Mode,
    [Parameter(Mandatory)][ValidatePattern('^[a-z0-9][a-z0-9-]+$')][string]$RunId,
    [string]$Modes = 'baseline,bounded-wait,kill-switch',
    [string]$Faults = 'none,transform-timeout,slow-original',
    [ValidateRange(1,5)][int]$Repetitions = 3,
    # Fargate's vCPU is slower than the local calibration host, so the AWS
    # phase lowers the stream rates; keep the same values for calibrate and measure.
    [string[]]$RunnerArgs = @('--hit-rate','5','--healthy-miss-rate','0.2','--poisoned-miss-rate','0.4')
)
. (Join-Path $PSScriptRoot 'common.ps1')
$requestPath = Join-Path $script:E3Root "local/$RunId-request.json"
if (Test-Path -LiteralPath $requestPath) { throw 'RunId already has a launch record; use a fresh RunId.' }
$runtime = Get-E3Runtime
$deadline = Assert-BeforeDeadline $runtime
$config = Get-E3Configuration
$image = Get-Content -LiteralPath (Join-Path $script:E3Root 'local/image.json') -Raw | ConvertFrom-Json
# One trial is 150 s plus prewarm/drain; the calibration subset is baseline × none/transform-timeout.
$combos = if ($Mode -eq 'calibrate') { 2 } else { ($Modes -split ',').Count * ($Faults -split ',').Count * $Repetitions }
$taskMinutes = [int][Math]::Ceiling($combos * 2.75 + 8)
if (($deadline - [DateTimeOffset]::UtcNow).TotalMinutes -lt ($taskMinutes + 10)) { throw "Less than $($taskMinutes + 10) minutes remain for this run and cleanup." }
$running = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','list-tasks','--cluster',$config.cluster)
if (@($running.taskArns).Count) { throw 'The E3 cluster already has an active task.' }
$command = @(
    'run','--storage','s3','--original-bucket',$config.result_bucket,'--derivative-bucket',$config.result_bucket,
    '--results-root','/tmp/e3-results','--run-id',$RunId,'--container-image',"$($image.repository)@$($image.digest)",
    '--upload-bucket',$config.result_bucket,'--upload-prefix','experiments/e3-failure-isolation/results-aws'
)
if ($Mode -eq 'calibrate') {
    $command += @('--calibration','--modes','baseline','--faults','none,transform-timeout','--repetitions','1')
} else {
    $command += @('--modes',$Modes,'--faults',$Faults,'--repetitions',"$Repetitions")
}
$command += $RunnerArgs
$overrides = @{containerOverrides=@(@{name='runner';command=$command})}
$request = @{
    cluster=$config.cluster; taskDefinition=$config.task_definition; launchType='FARGATE'; platformVersion='1.4.0'; count=1
    startedBy="e3-$($runtime.deployment_id)"; enableExecuteCommand=$false; propagateTags='TASK_DEFINITION'
    networkConfiguration=@{awsvpcConfiguration=@{subnets=@($config.subnet);securityGroups=@($config.security_group);assignPublicIp='ENABLED'}}
    overrides=$overrides
}
Save-E3Json $requestPath $request
$response = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','run-task','--cli-input-json',"file://$requestPath")
Save-E3Json (Join-Path $script:E3Root "local/$RunId-start.json") $response
if (@($response.failures).Count -or @($response.tasks).Count -ne 1) { throw 'ECS task launch failed.' }
$taskArn=$response.tasks[0].taskArn
$stopAt=[DateTimeOffset]::UtcNow.AddMinutes($taskMinutes)
if ($deadline -lt $stopAt) {$stopAt=$deadline}
try {
    $task=Wait-EcsTaskStopped $runtime.aws_profile $runtime.region $config.cluster $taskArn $stopAt
    Save-E3Json (Join-Path $script:E3Root "local/$RunId-stop.json") $task
} catch {
    Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','stop-task','--cluster',$config.cluster,'--task',$taskArn,'--reason','E3 run supervision failed') | Out-Null
    throw
} finally {
    Receive-E3Results $runtime $config | Out-Host
}
if (@($task.containers | Where-Object { $_.exitCode -ne 0 }).Count) { throw 'E3 task failed; raw recovered. Stop follow-up runs and inspect.' }
if (@($task.containers | Where-Object { $_.imageDigest -ne $runtime.image_digest }).Count) { throw 'Executed image digest differs from the pinned image.' }
Write-Host "E3 $Mode finished and recovered under local/recovered. Analyze independently before using the results."
