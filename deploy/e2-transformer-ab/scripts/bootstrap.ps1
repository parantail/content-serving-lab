[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[a-z0-9][a-z0-9-]{7,15}$')][string]$DeploymentId,
    [Parameter(Mandatory)][ValidateRange(0.01,3)][double]$ExpectedCostUsd,
    [string]$Profile = 'content-serving-lab-sandbox'
)
. (Join-Path $PSScriptRoot 'common.ps1')
$repo = (Resolve-Path (Join-Path $script:E2Root '../..')).Path
$dirty = @(Invoke-CheckedCommand git @('-C', $repo, 'status', '--porcelain'))
if ($dirty.Count) { throw 'Commit the verified checkpoint before bootstrap.' }
$commit = (Invoke-CheckedCommand git @('-C', $repo, 'rev-parse', 'HEAD')) -join ''
if (Test-Path -LiteralPath (Join-Path $script:E2Root 'runtime.auto.tfvars.json')) { throw 'Existing runtime configuration must be cleaned up first.' }
& (Join-Path $PSScriptRoot 'preflight.ps1') -DeploymentId $DeploymentId -ExpectedCostUsd $ExpectedCostUsd -Profile $Profile
$local = Join-Path $script:E2Root 'local'
New-Item -ItemType Directory -Path $local -Force | Out-Null
$deadline = [DateTimeOffset]::UtcNow.AddHours(2).ToString('o')
$runtime = @{ deployment_id=$DeploymentId; aws_profile=$Profile; region='ap-northeast-2'; expires_at=$deadline; image_digest='' }
Save-E2Json (Join-Path $script:E2Root 'runtime.auto.tfvars.json') $runtime
Save-E2Json (Join-Path $script:E2Root 'deadline.json') @{ expires_at=$deadline; commit=$commit; expected_cost_usd=$ExpectedCostUsd }
& (Join-Path $PSScriptRoot 'start-watchdog.ps1')
Invoke-Terraform $script:E2Root @('init', '-input=false', '-lockfile=readonly')
Invoke-Terraform $script:E2Root @('plan', '-input=false', '-target=aws_ecr_repository.runner', '-out=local/bootstrap.tfplan')
$plan = ((Invoke-CheckedCommand terraform @("-chdir=$script:E2Root",'show','-json','local/bootstrap.tfplan')) -join "`n") | ConvertFrom-Json
$changes = @($plan.resource_changes | Where-Object { $_.change.actions -join ',' -ne 'no-op' })
if ($changes.Count -ne 1 -or $changes[0].address -ne 'aws_ecr_repository.runner' -or ($changes[0].change.actions -join ',') -ne 'create') { throw 'Bootstrap plan must only create the E2 ECR repository.' }
Invoke-Terraform $script:E2Root @('apply', '-input=false', 'local/bootstrap.tfplan')
$repository = ((Invoke-CheckedCommand terraform @("-chdir=$script:E2Root",'output','-raw','repository_url')) -join '').Trim()
$registry = ($repository -split '/')[0]
# Password is passed over stdin and never captured in deployment artifacts.
& aws ecr get-login-password --profile $Profile --region 'ap-northeast-2' | & docker login --username AWS --password-stdin $registry
if ($LASTEXITCODE -ne 0) { throw 'ECR docker login failed.' }
Invoke-CheckedCommand docker @('build','--platform','linux/amd64','--provenance=false','-f',(Join-Path $repo 'Dockerfile.e2'),'--target','runtime','--build-arg',"GIT_COMMIT=$commit",'-t',"${repository}:$commit",$repo) | Out-Host
Invoke-CheckedCommand docker @('push',"${repository}:$commit") | Out-Host
$image = Invoke-AwsJson $Profile 'ap-northeast-2' @('ecr','describe-images','--repository-name',"content-serving-e2-$DeploymentId",'--image-ids',"imageTag=$commit")
$runtime.image_digest = $image.imageDetails[0].imageDigest
Save-E2Json (Join-Path $script:E2Root 'runtime.auto.tfvars.json') $runtime
Save-E2Json (Join-Path $local 'image.json') @{commit=$commit; digest=$runtime.image_digest; repository=$repository}
Write-Host "ECR image ready; deadline $deadline. Review and apply the full saved plan next."
