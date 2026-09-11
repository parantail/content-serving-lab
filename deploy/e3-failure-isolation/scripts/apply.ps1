[CmdletBinding()]
param([Parameter(Mandatory)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ReviewedPlanSHA256)
. (Join-Path $PSScriptRoot 'common.ps1')
Assert-BeforeDeadline (Get-E3Runtime) | Out-Null
$path = Join-Path $script:E3Root 'local/e3.tfplan'
if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $ReviewedPlanSHA256) { throw 'Plan hash differs from the reviewed plan.' }
Invoke-Terraform $script:E3Root @('apply','-input=false','local/e3.tfplan')
Save-E3Json (Join-Path $script:E3Root 'local/configuration.json') (Get-E3Configuration)
