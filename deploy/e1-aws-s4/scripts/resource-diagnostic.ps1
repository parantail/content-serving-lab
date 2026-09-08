[CmdletBinding()]
param()

. (Join-Path $PSScriptRoot "common.ps1")
$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runtime = Read-RuntimeConfiguration -ModuleRoot $moduleRoot
$deadline = Assert-BeforeDeadline -RuntimeConfiguration $runtime
if (($deadline - [DateTimeOffset]::UtcNow).TotalMinutes -lt 10) { throw "At least ten minutes must remain for diagnosis and cleanup." }
Push-Location $moduleRoot
try {
    $rawState = (& terraform show -json) -join [Environment]::NewLine
    if ($LASTEXITCODE -ne 0) { throw "Unable to read applied Terraform state." }
} finally { Pop-Location }
$state = $rawState | ConvertFrom-Json -Depth 100
$resources = @($state.values.root_module.resources)
$cluster = @($resources | Where-Object address -eq 'aws_ecs_cluster.experiment[0]')[0].values.arn
$definition = @($resources | Where-Object address -eq 'aws_ecs_task_definition.media[0]')[0].values
$service = @($resources | Where-Object address -eq 'aws_ecs_service.media["one"]')[0].values
$containerDef = @($definition.container_definitions | ConvertFrom-Json)[0]
$logGroup = $containerDef.logConfiguration.options.'awslogs-group'
$logPrefix = $containerDef.logConfiguration.options.'awslogs-stream-prefix'
$networkConfig = $service.network_configuration[0]
$network = "awsvpcConfiguration={subnets=[$(@($networkConfig.subnets) -join ',')],securityGroups=[$(@($networkConfig.security_groups) -join ',')],assignPublicIp=ENABLED}"
$existing = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('ecs','list-tasks','--cluster',$cluster,'--started-by',"e1-diag-$($runtime.deployment_id)")
if (@($existing.taskArns).Count -ne 0) { throw "A diagnostic task is already active." }
$overrides = @{ containerOverrides = @(@{ name='media-service'; environment=@(@{ name='E1_RESOURCE_DIAGNOSTIC'; value='true' }) }) } | ConvertTo-Json -Depth 6 -Compress
$run = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('ecs','run-task','--cluster',$cluster,'--task-definition',$definition.arn,'--launch-type','FARGATE','--network-configuration',$network,'--overrides',$overrides,'--started-by',"e1-diag-$($runtime.deployment_id)",'--enable-ecs-managed-tags','--propagate-tags','TASK_DEFINITION','--count','1')
if (@($run.failures).Count -ne 0 -or @($run.tasks).Count -ne 1) { throw "ECS did not start exactly one diagnostic task." }
$taskArn = [string]$run.tasks[0].taskArn
try {
    $task = Wait-EcsTaskStopped -Profile $runtime.aws_profile -Region $runtime.region -Cluster $cluster -TaskArn $taskArn -StopAt ([DateTimeOffset]::UtcNow.AddMinutes(5))
    $logStream = "$logPrefix/media-service/$(($taskArn -split '/')[-1])"
    $rows = @()
    for ($attempt = 0; $attempt -lt 12; $attempt++) {
        $logs = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('logs','get-log-events','--log-group-name',$logGroup,'--log-stream-name',$logStream,'--start-from-head')
        $rows = @($logs.events | ForEach-Object { $_.message })
        if (@($rows | Where-Object { $_ -match '"kind":"complete"' }).Count -eq 1) { break }
        Start-Sleep -Seconds 5
    }
    $recovery = Join-Path $moduleRoot "recovered-results\$($runtime.deployment_id)\resource-diagnostic"
    New-Item -ItemType Directory -Path $recovery -Force | Out-Null
    $rows | Set-Content -LiteralPath (Join-Path $recovery 'diagnostic.jsonl') -Encoding utf8NoBOM
    $container = @($task.containers | Where-Object name -eq 'media-service')[0]
    if ($null -eq $container.exitCode -or [int]$container.exitCode -ne 0) { throw "Diagnostic task failed; available logs were recovered locally." }
    $records = @($rows | ForEach-Object { $_ | ConvertFrom-Json })
    if (@($records | Where-Object kind -eq 'window').Count -ne 12 -or @($records | Where-Object kind -eq 'scope').Count -ne 4 -or @($records | Where-Object kind -eq 'complete').Count -ne 1) { throw "Diagnostic log recovery is incomplete." }
    [ordered]@{ deployment_id=$runtime.deployment_id; image=$definition.container_definitions | ConvertFrom-Json | ForEach-Object { ($_.image -split '@')[-1] }; runtime_image_digest=$container.imageDigest; platform_version=$task.platformVersion; cpu=$task.cpu; memory=$task.memory; recovered_at=[DateTimeOffset]::UtcNow.ToString('o') } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $recovery 'execution.json') -Encoding utf8NoBOM
    Write-Host 'Resource diagnostic completed: 12 windows and before/after scope records recovered. Review the values before accepting the measurement contract.'
} finally {
    $status = Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('ecs','describe-tasks','--cluster',$cluster,'--tasks',$taskArn)
    if (@($status.tasks | Where-Object lastStatus -ne 'STOPPED').Count -gt 0) {
        Invoke-AwsJson -Profile $runtime.aws_profile -Region $runtime.region -Arguments @('ecs','stop-task','--cluster',$cluster,'--task',$taskArn,'--reason','E1 resource diagnostic cleanup') | Out-Null
    }
}
