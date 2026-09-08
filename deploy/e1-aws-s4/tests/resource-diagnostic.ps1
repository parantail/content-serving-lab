Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Mock both native commands; this test never uses credentials or AWS APIs.
$diagnosticTestRoot = Join-Path ([IO.Path]::GetTempPath()) ('e1-diagnostic-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $diagnosticTestRoot 'scripts') -Force | Out-Null
foreach ($name in @('common.ps1','resource-diagnostic.ps1')) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot "../scripts/$name") -Destination (Join-Path $diagnosticTestRoot "scripts/$name")
}
@{ deployment_id='diagnostic-test'; aws_profile='mock'; region='ap-northeast-2'; expires_at=[DateTimeOffset]::UtcNow.AddHours(2).ToString('o') } | ConvertTo-Json | Set-Content (Join-Path $diagnosticTestRoot 'runtime.auto.tfvars.json')
function terraform {
    if (($args -join ' ') -ne 'show -json') { throw 'Unexpected Terraform operation' }
    $global:LASTEXITCODE = 0
    @{ values=@{root_module=@{resources=@(
        @{address='aws_ecs_cluster.experiment[0]'; values=@{arn='mock-cluster'}},
        @{address='aws_ecs_task_definition.media[0]'; values=@{arn='mock-definition';container_definitions=(@(@{image='mock@sha256:mock';logConfiguration=@{options=@{'awslogs-group'='mock-group';'awslogs-stream-prefix'='media'}}}) | ConvertTo-Json -Depth 8 -Compress)}},
        @{address='aws_ecs_service.media["one"]';values=@{network_configuration=@(@{subnets=@('mock-subnet');security_groups=@('mock-group')})}}
    )}}} | ConvertTo-Json -Depth 15 -Compress
}
function aws {
    if ($args -notcontains 'mock') { throw 'Mock profile is required' }
    $global:LASTEXITCODE = 0
    $result = switch ("$($args[0]) $($args[1])") {
        'ecs list-tasks' { @{taskArns=@()} }
        'ecs run-task' {
            $overrideIndex = [array]::IndexOf($args,'--overrides')
            $overrides = $args[$overrideIndex+1] | ConvertFrom-Json
            if ($overrides.containerOverrides[0].environment[0].name -ne 'E1_RESOURCE_DIAGNOSTIC') { throw 'Missing diagnostic opt-in' }
            @{failures=@();tasks=@(@{taskArn='mock/task/diagnostic'})}
        }
        'ecs describe-tasks' { @{failures=@();tasks=@(@{lastStatus='STOPPED';cpu='1024';memory='2048';platformVersion='mock';containers=@(@{name='media-service';exitCode=0;imageDigest='sha256:mock'})})} }
        'logs get-log-events' {
            $events = @(1..12 | ForEach-Object { @{message='{"kind":"window"}'} })
            $events += @(1..4 | ForEach-Object { @{message='{"kind":"scope"}'} })
            $events += @{message='{"kind":"complete"}'}
            @{events=$events}
        }
        default { throw 'Unexpected AWS operation' }
    }
    $result | ConvertTo-Json -Depth 15 -Compress
}
& (Join-Path $diagnosticTestRoot 'scripts/resource-diagnostic.ps1')
$recovery = Join-Path $diagnosticTestRoot 'recovered-results/diagnostic-test/resource-diagnostic'
if (@(Get-Content (Join-Path $recovery 'diagnostic.jsonl')).Count -ne 17) { throw 'Missing diagnostic records' }
$execution = Get-Content (Join-Path $recovery 'execution.json') -Raw | ConvertFrom-Json
if ($execution.cpu -ne '1024' -or $execution.memory -ne '2048') { throw 'Missing execution limits' }
Write-Host 'Resource diagnostic mock test passed: one-shot overrides, log recovery, completion records and execution limits.'
