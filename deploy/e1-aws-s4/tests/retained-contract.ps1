Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '../scripts/retained-contract.ps1')
$fixture = Join-Path $PSScriptRoot '../../../reports/e1-cache-stampede/aws-s4-resource-diagnostic'
$execution = Get-Content (Join-Path $fixture 'execution.json') -Raw | ConvertFrom-Json
Assert-RetainedDiagnostic -Directory $fixture -DeploymentId $execution.deployment_id -ImageDigest $execution.image
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('e1-retained-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null
Copy-Item (Join-Path $fixture 'execution.json') $testRoot
foreach ($case in @('source','scope','gap','duplicate','busy-zero','digest','checkpoint')) {
    $rows = @(Get-Content (Join-Path $fixture 'diagnostic.jsonl') | ForEach-Object { $_ | ConvertFrom-Json })
    switch ($case) {
        source { $rows[0].source = 'cgroup-v2-container-visible' }
        scope { $rows[0].scope.mount_found = $false }
        gap { $rows[-1].sample_gap_ms = 100 }
        duplicate { $rows[1] = $rows[0] }
        busy-zero { ($rows | Where-Object { $_.kind -eq 'window' -and $_.busy } | Select-Object -First 1).cgroup_cpu_ms = 0 }
        checkpoint { $rows[-1].git_commit = ('f' * 40) }
    }
    $rows | ForEach-Object { $_ | ConvertTo-Json -Depth 8 -Compress } | Set-Content (Join-Path $testRoot 'diagnostic.jsonl')
    $digest = if ($case -eq 'digest') { 'sha256:wrong' } else { $execution.image }
    $rejected = $false
    try { Assert-RetainedDiagnostic -Directory $testRoot -DeploymentId $execution.deployment_id -ImageDigest $digest } catch { $rejected = $true }
    if (-not $rejected) { throw "Accepted altered retained diagnostic: $case" }
}
Write-Host 'Retained diagnostic gate: valid fixture and seven rejection cases passed; no AWS calls.'
