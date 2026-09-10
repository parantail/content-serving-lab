[CmdletBinding()]
param([Parameter(Mandatory)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ReviewedPlanSHA256)
. (Join-Path $PSScriptRoot 'common.ps1')
Assert-BeforeDeadline (Get-E2Runtime) | Out-Null
$path = Join-Path $script:E2Root 'local/e2.tfplan'
if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $ReviewedPlanSHA256) { throw 'Plan hash differs from the reviewed plan.' }
Invoke-Terraform $script:E2Root @('apply','-input=false','local/e2.tfplan')
Save-E2Json (Join-Path $script:E2Root 'local/configuration.json') (Get-E2Configuration)
