[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[a-z0-9][a-z0-9-]{7,15}$')][string]$DeploymentId,
    [string]$Profile='content-serving-lab-sandbox'
)
. (Join-Path $PSScriptRoot 'common.ps1')
$region='ap-northeast-2'
$name="content-serving-e3-$DeploymentId"
$checks=[ordered]@{checked_at=[DateTimeOffset]::UtcNow.ToString('o'); deployment_id=$DeploymentId}
$state=@(Invoke-CheckedCommand terraform @("-chdir=$script:E3Root",'state','list'))
$checks['terraform_resources']=$state.Count
foreach ($entry in @(
    @('vpcs','Vpcs'), @('subnets','Subnets'), @('internet-gateways','InternetGateways'),
    @('route-tables','RouteTables'), @('security-groups','SecurityGroups'), @('vpc-endpoints','VpcEndpoints')
)) {
    $response=Invoke-AwsJson $Profile $region @('ec2',"describe-$($entry[0])",'--filters',"Name=tag:DeploymentId,Values=$DeploymentId")
    $items=@($response.($entry[1]))
    if ($entry[0] -eq 'vpc-endpoints') {$items=@($items | Where-Object State -ne 'deleted')}
    $checks[$entry[0]]=$items.Count
}
$buckets=Invoke-AwsJson $Profile $region @('s3api','list-buckets')
$checks['buckets']=@($buckets.Buckets | Where-Object Name -eq $name).Count
$repos=Invoke-AwsJson $Profile $region @('ecr','describe-repositories')
$checks['repositories']=@($repos.repositories | Where-Object repositoryName -eq $name).Count
$logs=Invoke-AwsJson $Profile $region @('logs','describe-log-groups','--log-group-name-prefix',"/content-serving/e3/$DeploymentId")
$checks['log_groups']=@($logs.logGroups).Count
$roles=Invoke-AwsJson $Profile $region @('iam','list-roles')
$checks['roles']=@($roles.Roles | Where-Object { $_.RoleName -in @("$name-execution","$name-runner") }).Count
$defs=Invoke-AwsJson $Profile $region @('ecs','list-task-definitions','--family-prefix',$name,'--status','ACTIVE')
$checks['active_task_definitions']=@($defs.taskDefinitionArns).Count
$clusters=Invoke-AwsJson $Profile $region @('ecs','describe-clusters','--clusters',$name)
if (@($clusters.failures | Where-Object reason -ne 'MISSING').Count) { throw 'ECS residual check failed.' }
$checks['active_clusters']=@($clusters.clusters | Where-Object { $_.status -ne 'INACTIVE' -or $_.runningTasksCount -gt 0 -or $_.pendingTasksCount -gt 0 -or $_.activeServicesCount -gt 0 }).Count
$configuration=Join-Path $script:E3Root 'local/configuration.json'
if (Test-Path -LiteralPath $configuration) {
    $config=Get-Content -LiteralPath $configuration -Raw | ConvertFrom-Json
    $interfaces=Invoke-AwsJson $Profile $region @('ec2','describe-network-interfaces','--filters',"Name=vpc-id,Values=$($config.vpc)")
    $checks['network_interfaces']=@($interfaces.NetworkInterfaces).Count
}
$nonzero=@($checks.GetEnumerator() | Where-Object { $_.Key -notin @('checked_at','deployment_id') -and $_.Value -ne 0 })
$checks['valid']=$nonzero.Count -eq 0
Save-E3Json (Join-Path $script:E3Root 'local/residual-check.json') $checks
if ($nonzero.Count) { throw "E3 residual resources: $($nonzero.Key -join ', ')" }
Write-Host 'Terraform state and service-specific E3 residual counts are all zero.'
