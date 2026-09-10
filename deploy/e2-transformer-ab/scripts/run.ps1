[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('diagnose','validate','calibrate','measure','quality')][string]$Mode,
    [Parameter(Mandatory)][ValidatePattern('^[a-z0-9][a-z0-9-]+$')][string]$RunId
)
. (Join-Path $PSScriptRoot 'common.ps1')
$requestPath = Join-Path $script:E2Root "local/$RunId-request.json"
if (Test-Path -LiteralPath $requestPath) { throw 'RunId already has a launch record; use a fresh RunId.' }
$runtime = Get-E2Runtime
$deadline = Assert-BeforeDeadline $runtime
$config = Get-E2Configuration
# 210-minute loop + 5-minute Task overhead + 15-minute quality allowance + 10-minute cleanup margin.
if ($Mode -eq 'measure' -and ($deadline-[DateTimeOffset]::UtcNow).TotalMinutes -lt 240) { throw 'Less than 240 minutes remains for measurement, quality, and cleanup.' }
$running = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','list-tasks','--cluster',$config.cluster)
if (@($running.taskArns).Count) { throw 'The E2 cluster already has an active task.' }
$overrides = @{containerOverrides=@(@{name='runner';command=@('-mode',$Mode,'-output',"/results/$RunId",'-cohort','aws')})}
$request = @{
    cluster=$config.cluster; taskDefinition=$config.task_definition; launchType='FARGATE'; platformVersion='1.4.0'; count=1
    startedBy="e2-$($runtime.deployment_id)"; enableExecuteCommand=$false; propagateTags='TASK_DEFINITION'
    networkConfiguration=@{awsvpcConfiguration=@{subnets=@($config.subnet);securityGroups=@($config.security_group);assignPublicIp='ENABLED'}}
    overrides=$overrides
}
Save-E2Json $requestPath $request
$response = Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','run-task','--cli-input-json',"file://$requestPath")
Save-E2Json (Join-Path $script:E2Root "local/$RunId-start.json") $response
if (@($response.failures).Count -or @($response.tasks).Count -ne 1) { throw 'ECS task launch failed.' }
$taskArn=$response.tasks[0].taskArn
$taskMinutes = if ($Mode -eq 'measure') { 215 } else { 63 }
$stopAt=[DateTimeOffset]::UtcNow.AddMinutes($taskMinutes)
if ($deadline -lt $stopAt) {$stopAt=$deadline}
try {
    $task=Wait-EcsTaskStopped $runtime.aws_profile $runtime.region $config.cluster $taskArn $stopAt
    Save-E2Json (Join-Path $script:E2Root "local/$RunId-stop.json") $task
} catch {
    Invoke-AwsJson $runtime.aws_profile $runtime.region @('ecs','stop-task','--cluster',$config.cluster,'--task',$taskArn,'--reason','E2 run supervision failed') | Out-Null
    throw
} finally {
    Receive-E2Results $runtime $config | Out-Host
}
if (@($task.containers | Where-Object { $_.exitCode -ne 0 }).Count) { throw 'E2 task failed; raw recovered. Stop follow-up runs and inspect.' }
if (@($task.containers | Where-Object { $_.imageDigest -ne $runtime.image_digest }).Count) { throw 'Executed image digest differs from the pinned image.' }
Write-Host "E2 $Mode finished and recovered. Validate completion.json and analyze independently."
