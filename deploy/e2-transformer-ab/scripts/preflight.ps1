[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern("^[a-z0-9][a-z0-9-]{7,15}$")][string]$DeploymentId,
    [Parameter(Mandatory)][ValidateRange(0.01, 3.00)][double]$ExpectedCostUsd,
    [string]$Profile = "content-serving-lab-sandbox",
    [ValidateSet("ap-northeast-2")][string]$Region = "ap-northeast-2",
    [string[]]$AvailabilityZones = @("ap-northeast-2a"),
    [string]$NamePrefix = "content-serving-e2",
    [ValidateSet(0, 2)][int]$ExpectedTaggedResources = 0
)

. (Join-Path $PSScriptRoot "common.ps1")

Assert-CommandAvailable -Name "aws"
Assert-CommandAvailable -Name "terraform"
Assert-CommandAvailable -Name "docker"

$awsVersionText = (& aws --version 2>&1) -join " "
if ($LASTEXITCODE -ne 0 -or $awsVersionText -notmatch "aws-cli/(?<version>\d+\.\d+\.\d+)") {
    throw "Unable to determine the AWS CLI version."
}
$awsVersion = [Version]$Matches.version
if ($awsVersion -lt [Version]"2.32.0") {
    throw "AWS CLI 2.32.0 or later is required; found $awsVersion."
}

$terraformVersionText = (& terraform version -json) -join [Environment]::NewLine
if ($LASTEXITCODE -ne 0) {
    throw "Unable to determine the Terraform version."
}
$terraformVersion = [Version](($terraformVersionText | ConvertFrom-Json).terraform_version)
if ($terraformVersion -lt [Version]"1.15.0" -or $terraformVersion -ge [Version]"1.17.0") {
    throw "Terraform >= 1.15.0 and < 1.17.0 is required; found $terraformVersion."
}

& docker version --format "{{.Client.Version}}" | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Docker is installed but unavailable."
}

$configuredRegion = ((& aws configure get region --profile $Profile) -join "").Trim()
if ($LASTEXITCODE -ne 0 -or $configuredRegion -ne $Region) {
    throw "AWS profile '$Profile' must be configured for $Region."
}

$credentialReport = (& aws configure list --profile $Profile 2>&1) -join [Environment]::NewLine
if ($LASTEXITCODE -ne 0 -or $credentialReport -notmatch "(?i)(login|custom-process)") {
    throw "AWS profile '$Profile' must use aws login credentials or the documented credential_process fallback."
}

$identity = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @("sts", "get-caller-identity")
if ($null -eq $identity -or $identity.Arn -notmatch ":user/(?<path>.+)$") {
    throw "The active principal must be the dedicated sandbox IAM User authenticated with MFA and aws login."
}
$iamUserName = ($Matches.path -split "/")[-1]

$mfaDevices = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "iam", "list-mfa-devices", "--user-name", $iamUserName
)
if (@($mfaDevices.MFADevices).Count -lt 1) {
    throw "The deployment IAM User has no MFA device."
}

$administratorPolicyArn = "arn:aws:iam::aws:policy/AdministratorAccess"
$directPolicies = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "iam", "list-attached-user-policies", "--user-name", $iamUserName
)
$hasAdministratorAccess = @($directPolicies.AttachedPolicies | Where-Object PolicyArn -eq $administratorPolicyArn).Count -gt 0
if (-not $hasAdministratorAccess) {
    $groups = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
        "iam", "list-groups-for-user", "--user-name", $iamUserName
    )
    foreach ($group in @($groups.Groups)) {
        $groupPolicies = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
            "iam", "list-attached-group-policies", "--group-name", $group.GroupName
        )
        if (@($groupPolicies.AttachedPolicies | Where-Object PolicyArn -eq $administratorPolicyArn).Count -gt 0) {
            $hasAdministratorAccess = $true
            break
        }
    }
}
if (-not $hasAdministratorAccess) {
    throw "AdministratorAccess is not attached to the deployment IAM User or one of its groups."
}

$budgets = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "budgets", "describe-budgets", "--account-id", [string]$identity.Account
)
$budget = @($budgets.Budgets | Where-Object {
    $_.BudgetType -eq "COST" -and $_.TimeUnit -eq "MONTHLY" -and $_.BudgetLimit.Unit -eq "USD" -and [double]$_.BudgetLimit.Amount -eq 10.0
}) | Select-Object -First 1
if ($null -eq $budget) {
    throw "A monthly US`$10 COST budget is required for the sandbox account."
}

$notifications = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "budgets", "describe-notifications-for-budget",
    "--account-id", [string]$identity.Account,
    "--budget-name", [string]$budget.BudgetName
)
foreach ($threshold in @(50, 80, 100)) {
    $notification = @($notifications.Notifications | Where-Object {
        $thresholdTypeProperty = $_.PSObject.Properties["ThresholdType"]
        $usesPercentageThreshold = $null -eq $thresholdTypeProperty -or $thresholdTypeProperty.Value -eq "PERCENTAGE"
        $_.NotificationType -eq "ACTUAL" -and
        $usesPercentageThreshold -and
        [double]$_.Threshold -eq $threshold
    }) | Select-Object -First 1
    if ($null -eq $notification) {
        throw "The US`$10 budget is missing its ACTUAL $threshold% notification."
    }
    $notificationFields = @(
        "NotificationType=$($notification.NotificationType)"
        "ComparisonOperator=$($notification.ComparisonOperator)"
        "Threshold=$($notification.Threshold)"
    )
    $thresholdTypeProperty = $notification.PSObject.Properties["ThresholdType"]
    if ($null -ne $thresholdTypeProperty) {
        $notificationFields += "ThresholdType=$($thresholdTypeProperty.Value)"
    }
    $notificationKey = $notificationFields -join ","
    $subscribers = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
        "budgets", "describe-subscribers-for-notification",
        "--account-id", [string]$identity.Account,
        "--budget-name", [string]$budget.BudgetName,
        "--notification", $notificationKey
    )
    if (@($subscribers.Subscribers).Count -lt 1) {
        throw "The US`$10 budget's ACTUAL $threshold% notification has no subscriber."
    }
}

$quotas = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "service-quotas", "list-service-quotas", "--service-code", "fargate"
)
$quota = @($quotas.Quotas | Where-Object QuotaName -eq "Fargate On-Demand vCPU resource count") | Select-Object -First 1
if ($null -eq $quota -or [double]$quota.Value -lt 1.0) {
    throw "Fargate On-Demand vCPU quota must be at least 1 in $Region."
}

$publicAccess = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "s3control", "get-public-access-block", "--account-id", [string]$identity.Account
)
$block = $publicAccess.PublicAccessBlockConfiguration
if (-not ($block.BlockPublicAcls -and $block.IgnorePublicAcls -and $block.BlockPublicPolicy -and $block.RestrictPublicBuckets)) {
    throw "All four account-level S3 Block Public Access controls must be enabled."
}

$zones = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "ec2", "describe-availability-zones", "--filters", "Name=state,Values=available"
)
$availableZoneNames = @($zones.AvailabilityZones | ForEach-Object ZoneName)
foreach ($zone in $AvailabilityZones) {
    if ($zone -notin $availableZoneNames) {
        throw "Availability Zone $zone is not available to this account."
    }
}

$tagged = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "resourcegroupstaggingapi", "get-resources",
    "--tag-filters", "Key=Project,Values=content-serving-lab", "Key=Experiment,Values=e2-transformer-ab"
)
$currentMarker = "$NamePrefix-$DeploymentId"
$liveTagged = @(Get-LiveTaggedResources -Resources @($tagged.ResourceTagMappingList) -Profile $Profile -Region $Region)
$unrelated = @($liveTagged | Where-Object { $_.ResourceARN -notlike "*$currentMarker*" })
if ($unrelated.Count -gt 0) {
    throw "Unrelated resources already use the E2 experiment tags. Remove or retag them before deploying."
}
$currentTaggedCount = $liveTagged.Count
if ($currentTaggedCount -ne $ExpectedTaggedResources) {
    throw "Expected $ExpectedTaggedResources tagged resources at this workflow stage; found $currentTaggedCount. Refusing stale or partial infrastructure."
}

Write-Host "AWS CLI 2.32.0 or later: ready"
Write-Host "AWS CLI profile: ready"
Write-Host "Authentication: IAM User + MFA + aws login/credential_process"
Write-Host "Region: $Region"
Write-Host "US`$10 Budget with 50/80/100% actual alerts: ready"
Write-Host "Fargate On-Demand vCPU quota: $([double]$quota.Value)"
Write-Host "AdministratorAccess: attached"
Write-Host "S3 account Block Public Access: all enabled"
Write-Host "Reviewed maximum expected cost: US`$$ExpectedCostUsd"
