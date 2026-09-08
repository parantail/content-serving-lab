. (Join-Path $PSScriptRoot '../scripts/common.ps1')

# In-memory service API responses; never calls AWS.
function Invoke-AwsJson {
    param($Profile, $Region, $Arguments)
    if ($script:rejectRead) { throw 'mock API unavailable' }
    return $script:response
}

$script:rejectRead = $false
$cases = @(
    @{ Path = 'ec2:security-group-rule/sgr-test'; Live = @{ SecurityGroupRules = @(@{ SecurityGroupRuleId = 'sgr-test' }) }; Gone = @{ SecurityGroupRules = @() } },
    @{ Path = 'ec2:vpc-endpoint/vpce-test'; Live = @{ VpcEndpoints = @(@{ VpcEndpointId = 'vpce-test'; State = 'available' }) }; Gone = @{ VpcEndpoints = @(@{ VpcEndpointId = 'vpce-test'; State = 'deleted' }) } },
    @{ Path = 'ecs:cluster/test'; Live = @{ failures = @(); clusters = @(@{ status = 'ACTIVE'; runningTasksCount = 0; pendingTasksCount = 0; activeServicesCount = 0 }) }; Gone = @{ failures = @(); clusters = @(@{ status = 'INACTIVE'; runningTasksCount = 0; pendingTasksCount = 0; activeServicesCount = 0 }) } },
    @{ Path = 'ecs:service/test/service'; Live = @{ failures = @(); services = @(@{ status = 'DRAINING'; runningCount = 0; pendingCount = 0 }) }; Gone = @{ failures = @(); services = @(@{ status = 'INACTIVE'; runningCount = 0; pendingCount = 0 }) } },
    @{ Path = 'ecs:task/test/task'; Live = @{ failures = @(); tasks = @(@{ lastStatus = 'PENDING' }) }; Gone = @{ failures = @(); tasks = @(@{ lastStatus = 'STOPPED' }) } },
    @{ Path = 'ecs:task-definition/test:1'; Live = @{ taskDefinition = @{ status = 'ACTIVE' } }; Gone = @{ taskDefinition = @{ status = 'INACTIVE' } } }
)
foreach ($case in $cases) {
    $service, $path = $case.Path -split ':', 2
    $resource = [pscustomobject]@{ ResourceARN = "arn:aws:${service}:ap-northeast-2:000000000000:$path" }
    foreach ($state in @('Live', 'Gone')) {
        $script:response = $case[$state] | ConvertTo-Json -Depth 10 | ConvertFrom-Json
        $actual = @(Get-LiveTaggedResources -Resources @($resource) -Profile test -Region ap-northeast-2)
        $expected = if ($state -eq 'Live') { 1 } else { 0 }
        if ($actual.Count -ne $expected) { throw "$($case.Path) $state classification failed" }
    }
}
$unknown = [pscustomobject]@{ ResourceARN = 'arn:aws:example:ap-northeast-2:000000000000:unknown/test' }
if (@(Get-LiveTaggedResources -Resources @($unknown) -Profile test -Region ap-northeast-2).Count -ne 1) { throw 'Unknown types must block cleanup.' }
$script:rejectRead = $true
$rejected = $false
try { Get-LiveTaggedResources -Resources @($resource) -Profile test -Region ap-northeast-2 | Out-Null }
catch { $rejected = $true }
if (-not $rejected) { throw 'API failures must block cleanup.' }
Write-Host 'Tagged resource checks passed: 12 lifecycle cases, unknown type, API failure.'
