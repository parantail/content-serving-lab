Set-StrictMode -Version Latest

function Assert-RetainedDiagnostic {
    param([string]$Directory, [string]$DeploymentId, [string]$ImageDigest)
    $execution = Get-Content -LiteralPath (Join-Path $Directory 'execution.json') -Raw | ConvertFrom-Json
    $rows = @(Get-Content -LiteralPath (Join-Path $Directory 'diagnostic.jsonl') | ForEach-Object { $_ | ConvertFrom-Json })
    if ($execution.deployment_id -ne $DeploymentId -or $execution.image -ne $ImageDigest -or $execution.runtime_image_digest -ne $ImageDigest -or $execution.cpu -ne '1024' -or $execution.memory -ne '2048' -or $execution.platform_version -ne '1.4.0') { throw 'Retained diagnostic execution does not match this deployment.' }
    $complete = @($rows | Where-Object kind -eq complete)
    $scopes = @($rows | Where-Object kind -eq scope)
    $windows = @($rows | Where-Object kind -eq window)
    if ($rows.Count -ne 17 -or $complete.Count -ne 1 -or $scopes.Count -ne 4 -or $windows.Count -ne 12) { throw 'Retained diagnostic records are incomplete.' }
    if ($complete[0].git_commit -notmatch '^[0-9a-f]{40}$' -or -not $complete[0].git_commit.StartsWith($DeploymentId) -or $complete[0].sample_gap_ms -ne 50 -or $complete[0].windows -ne 12) { throw 'Retained diagnostic checkpoint or sample interval mismatch.' }
    foreach ($row in $rows) { if ($row.source -ne 'cgroup-v1-container-visible') { throw 'Retained diagnostic requires the validated cgroup v1 source.' } }
    foreach ($stage in @('before','after')) {
        foreach ($counter in @('cpu','memory')) {
            $matches = @($scopes | Where-Object { $_.stage -eq $stage -and $_.counter -eq $counter })
            if ($matches.Count -ne 1) { throw 'Retained diagnostic scope pairs are incomplete.' }
            $scope = $matches[0].scope
            if (-not $scope.mount_found -or -not $scope.membership_found -or -not $scope.membership_equals_mount_root -or -not $scope.self_direct_member -or $scope.visible_direct_members -ne 1 -or $scope.hidden_direct_members -ne 0 -or $scope.child_groups -ne 0) { throw 'Retained diagnostic scope is not verified.' }
        }
    }
    foreach ($rep in 1..3) {
        foreach ($busy in @($false,$true)) {
            foreach ($sampling in @($false,$true)) {
                $matches = @($windows | Where-Object { $_.repetition -eq $rep -and $_.busy -eq $busy -and $_.sampling -eq $sampling })
                if ($matches.Count -ne 1) { throw 'Retained diagnostic window pairs are incomplete.' }
                $window = $matches[0]
                if ($window.wall_ms -le 0 -or $window.cgroup_cpu_ms -lt 0 -or $window.process_cpu_ms -lt 0 -or ($busy -and ($window.cgroup_cpu_ms -le 0 -or $window.process_cpu_ms -le 0)) -or ($sampling -and $window.samples -lt 2)) { throw 'Retained diagnostic CPU or sampling failed.' }
            }
        }
    }
}
