[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern("^[a-z0-9][a-z0-9-]{7,15}$")][string]$DeploymentId,
    [string]$Profile = "content-serving-lab-sandbox",
    [ValidateSet("ap-northeast-2")][string]$Region = "ap-northeast-2",
    [string]$NamePrefix = "content-serving-e1-s4"
)

. (Join-Path $PSScriptRoot "common.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$resourceMarker = "$NamePrefix-$DeploymentId"
$namedResource = $resourceMarker.Substring(0, [Math]::Min(32, $resourceMarker.Length))
$targetGroupPrefix = $namedResource.Substring(0, [Math]::Min(26, $namedResource.Length))
$bucketMarker = ($resourceMarker -replace "_", "-")

Push-Location $moduleRoot
try {
    $stateEntries = @(& terraform state list)
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to inspect Terraform state after destroy."
    }
}
finally {
    Pop-Location
}
if ($stateEntries.Count -gt 0) {
    throw "Terraform state still contains managed resources after destroy."
}

$remainingCount = -1
for ($attempt = 1; $attempt -le 6; $attempt++) {
    $tagged = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
        "resourcegroupstaggingapi", "get-resources",
        "--tag-filters", "Key=Project,Values=content-serving-lab", "Key=Experiment,Values=e1-aws-s4"
    )
    $taggedCurrent = @(Get-LiveTaggedResources -Resources @($tagged.ResourceTagMappingList) -Profile $Profile -Region $Region)

    $repositories = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("ecr", "describe-repositories")
    $repositoryCurrent = @($repositories.repositories | Where-Object repositoryName -like "$resourceMarker/*")

    $buckets = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("s3api", "list-buckets")
    $bucketCurrent = @($buckets.Buckets | Where-Object Name -like "$bucketMarker-*")

    $roles = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("iam", "list-roles", "--max-items", "1000")
    $roleCurrent = @($roles.Roles | Where-Object RoleName -like "$namedResource-*")

    $loadBalancers = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("elbv2", "describe-load-balancers")
    $loadBalancerCurrent = @($loadBalancers.LoadBalancers | Where-Object LoadBalancerName -eq $namedResource)
    $targetGroups = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("elbv2", "describe-target-groups")
    $targetGroupCurrent = @($targetGroups.TargetGroups | Where-Object TargetGroupName -like "$targetGroupPrefix-*")

    $clusters = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("ecs", "list-clusters")
    $clusterCurrent = @($clusters.clusterArns | Where-Object { $_ -like "*/$namedResource" })

    $vpcs = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
        "ec2", "describe-vpcs",
        "--filters", "Name=tag:Project,Values=content-serving-lab", "Name=tag:Experiment,Values=e1-aws-s4"
    )
    $vpcCurrent = @($vpcs.Vpcs | Where-Object { $_.Tags.Value -contains $namedResource })

    $logs = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
        "logs", "describe-log-groups", "--log-group-name-prefix", "/ecs/$namedResource/"
    )
    $logCurrent = @($logs.logGroups)

    $remainingCount = @(
        $taggedCurrent
        $repositoryCurrent
        $bucketCurrent
        $roleCurrent
        $loadBalancerCurrent
        $targetGroupCurrent
        $clusterCurrent
        $vpcCurrent
        $logCurrent
    ).Count
    if ($remainingCount -eq 0) {
        break
    }
    if ($attempt -lt 6) {
        Start-Sleep -Seconds 10
    }
}

if ($remainingCount -ne 0) {
    throw "AWS still reports $remainingCount matching resource records after destroy. Inspect the sandbox account without publishing IDs or ARNs."
}
Write-Host "Destroy verification passed: Terraform state is empty and no live matching resources remain; deleted/inactive records were verified through service APIs."
