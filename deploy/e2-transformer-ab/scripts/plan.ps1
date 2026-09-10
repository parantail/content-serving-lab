. (Join-Path $PSScriptRoot 'common.ps1')
$runtime = Get-E2Runtime
Assert-BeforeDeadline $runtime | Out-Null
if ($runtime.image_digest -notmatch '^sha256:[0-9a-f]{64}$') { throw 'Bootstrap image digest is missing.' }
Invoke-Terraform $script:E2Root @('validate')
Invoke-Terraform $script:E2Root @('plan', '-input=false', '-out=local/e2.tfplan')
$plan = ((Invoke-CheckedCommand terraform @("-chdir=$script:E2Root",'show','-json','local/e2.tfplan')) -join "`n") | ConvertFrom-Json
$changes = @($plan.resource_changes | Where-Object { $_.change.actions -join ',' -ne 'no-op' })
if (@($changes | Where-Object { ($_.change.actions -join ',') -ne 'create' }).Count) { throw 'Only new E2 resources are allowed in the initial full plan.' }
$summary = @($changes | ForEach-Object { [ordered]@{address=$_.address; action=($_.change.actions -join ',')} })
Save-E2Json (Join-Path $script:E2Root 'local/plan-review.json') $summary
$summary | Format-Table
Get-FileHash -LiteralPath (Join-Path $script:E2Root 'local/e2.tfplan') -Algorithm SHA256 | Select-Object Hash
