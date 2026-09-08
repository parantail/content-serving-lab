Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('e1-run-task-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $testRoot 'scripts') -Force | Out-Null
foreach ($name in @('common.ps1','run-task.ps1','retained-contract.ps1')) { Copy-Item (Join-Path $PSScriptRoot "../scripts/$name") (Join-Path $testRoot "scripts/$name") }
$global:e1RunTestConfiguration = @{cluster='mock';task_definition='mock';runner_family='mock';subnets=@('mock');security_group='mock';result_bucket='mock';result_prefix='results';run_mode='calibration';run_id='calibration-test-run';media_digest='mock'}
@{deployment_id='test-run';aws_profile='mock';region='ap-northeast-2';expires_at=[DateTimeOffset]::UtcNow.AddHours(2).ToString('o')} | ConvertTo-Json | Set-Content (Join-Path $testRoot 'runtime.auto.tfvars.json')
$global:e1RunTestExisting = 0
$global:e1RunTestStarted = 0
function terraform { $global:LASTEXITCODE=0; $global:e1RunTestConfiguration | ConvertTo-Json -Compress }
function aws {
    $global:LASTEXITCODE=0
    switch ("$($args[0]) $($args[1])") {
        's3api list-objects-v2' { @{KeyCount=$global:e1RunTestExisting} | ConvertTo-Json }
        'ecs list-tasks' { '{"taskArns":[]}' }
        'ecs run-task' { $global:e1RunTestStarted++; '{"failures":[],"tasks":[{"taskArn":"mock/task"}]}' }
        'ecs describe-tasks' { '{"failures":[],"tasks":[{"lastStatus":"STOPPED","containers":[{"name":"load-generator","exitCode":0}]}]}' }
        's3 sync' {
            $target = Join-Path $args[3] $global:e1RunTestConfiguration.run_id
            New-Item -ItemType Directory -Path $target -Force | Out-Null
            '{}' | Set-Content (Join-Path $target 'run.json')
        }
        default { throw 'Unexpected mock AWS operation' }
    }
}
function Expect-Rejection([scriptblock]$Action) {
    $before=$global:e1RunTestStarted; $rejected=$false
    try { & $Action } catch { $rejected=$true }
    if (-not $rejected -or $global:e1RunTestStarted -ne $before) { throw 'Unsafe run-task gate accepted execution' }
}
$entry=Join-Path $testRoot 'scripts/run-task.ps1'
Expect-Rejection { & $entry -RunMode retained }
$global:e1RunTestExisting=1
Expect-Rejection { & $entry }
$global:e1RunTestExisting=0
& $entry
if ($global:e1RunTestStarted -ne 1) { throw 'Calibration failed to start once' }
Expect-Rejection { & $entry }
$global:e1RunTestConfiguration.run_mode='retained'; $global:e1RunTestConfiguration.run_id='retained-test-run'
Expect-Rejection { & $entry -RunMode retained }
Write-Host 'Run-task mock passed: mode mismatch, remote/local collisions, diagnostic requirement and successful calibration.'
