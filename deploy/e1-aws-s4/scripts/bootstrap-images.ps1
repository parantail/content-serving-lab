[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern("^[a-z0-9][a-z0-9-]{7,15}$")][string]$DeploymentId,
    [Parameter(Mandatory)][ValidateRange(0.01, 9.99)][double]$ExpectedCostUsd,
    [string]$Profile = "content-serving-lab-sandbox",
    [ValidateSet("ap-northeast-2")][string]$Region = "ap-northeast-2"
)

. (Join-Path $PSScriptRoot "common.ps1")

$moduleRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$repositoryRoot = (Resolve-Path (Join-Path $moduleRoot "..\..")).Path
$runtimePath = Join-Path $moduleRoot "runtime.auto.tfvars.json"
$deadlinePath = Join-Path $moduleRoot "deadline.json"

if (Test-Path -LiteralPath $runtimePath) {
    throw "runtime.auto.tfvars.json already exists. Destroy or finish the current deployment first."
}

& (Join-Path $PSScriptRoot "preflight.ps1") `
    -DeploymentId $DeploymentId `
    -ExpectedCostUsd $ExpectedCostUsd `
    -Profile $Profile `
    -Region $Region

$gitStatus = (& git -C $repositoryRoot status --porcelain=v1 --untracked-files=normal) -join [Environment]::NewLine
if ($LASTEXITCODE -ne 0 -or -not [string]::IsNullOrWhiteSpace($gitStatus)) {
    throw "The public repository must be clean before building immutable experiment images."
}
$gitCommit = ((& git -C $repositoryRoot rev-parse HEAD) -join "").Trim().ToLowerInvariant()
if ($LASTEXITCODE -ne 0 -or $gitCommit -notmatch "^[0-9a-f]{40}$") {
    throw "Unable to resolve the public repository commit."
}
if ($DeploymentId -ne $gitCommit.Substring(0, 12)) {
    throw "DeploymentId must equal the first 12 characters of the clean public commit."
}

$startedAt = [DateTimeOffset]::UtcNow
$expiresAt = $startedAt.AddHours(2)
$startedText = $startedAt.ToString("o")
$expiresText = $expiresAt.ToString("o")
$bootstrapRuntime = [ordered]@{
    deployment_id       = $DeploymentId
    aws_profile         = $Profile
    region              = $Region
    enable_environment  = $false
    media_image_digest  = $null
    runner_image_digest = $null
    apply_started_at    = $startedText
    expires_at          = $expiresText
    expected_cost_usd   = $ExpectedCostUsd
}
$bootstrapRuntime | ConvertTo-Json | Set-Content -LiteralPath $runtimePath -Encoding utf8NoBOM
[ordered]@{
    deployment_id = $DeploymentId
    profile       = $Profile
    region        = $Region
    expires_at    = $expiresText
} | ConvertTo-Json | Set-Content -LiteralPath $deadlinePath -Encoding utf8NoBOM
& (Join-Path $PSScriptRoot "start-watchdog.ps1")

$bootstrapVariables = @(
    "-var=deployment_id=$DeploymentId",
    "-var=aws_profile=$Profile",
    "-var=enable_environment=false",
    "-var=apply_started_at=$startedText",
    "-var=expires_at=$expiresText",
    "-var=expected_cost_usd=$ExpectedCostUsd"
)

Invoke-Terraform -ModuleRoot $moduleRoot -Arguments @("init", "-input=false")
Invoke-Terraform -ModuleRoot $moduleRoot -Arguments (@("apply", "-input=false", "-auto-approve") + $bootstrapVariables)

Push-Location $moduleRoot
try {
    $repositoryJson = (& terraform output -json ecr_repository_urls) -join [Environment]::NewLine
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to read Terraform ECR outputs."
    }
}
finally {
    Pop-Location
}
$repositories = $repositoryJson | ConvertFrom-Json
$registry = ([string]$repositories.media -split "/")[0]

$loginPassword = (& aws ecr get-login-password --profile $Profile --region $Region --no-cli-pager) -join ""
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($loginPassword)) {
    throw "Unable to obtain the temporary ECR login password."
}
$loginPassword | & docker login --username AWS --password-stdin $registry | Out-Null
$loginPassword = $null
if ($LASTEXITCODE -ne 0) {
    throw "Docker could not authenticate to ECR."
}

$mediaTag = "$($repositories.media):$gitCommit"
$runnerTag = "$($repositories.runner):$gitCommit"
& docker build --pull --platform linux/amd64 --target service --build-arg "GIT_COMMIT=$gitCommit" --tag $mediaTag $repositoryRoot
if ($LASTEXITCODE -ne 0) { throw "Media image build failed." }
& docker build --pull --platform linux/amd64 --target experiment-aws-s4 --build-arg "GIT_COMMIT=$gitCommit" --tag $runnerTag $repositoryRoot
if ($LASTEXITCODE -ne 0) { throw "Runner image build failed." }
& docker push $mediaTag
if ($LASTEXITCODE -ne 0) { throw "Media image push failed." }
& docker push $runnerTag
if ($LASTEXITCODE -ne 0) { throw "Runner image push failed." }

$mediaRepositoryName = ([string]$repositories.media -split "/", 2)[1]
$runnerRepositoryName = ([string]$repositories.runner -split "/", 2)[1]
$mediaImage = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "ecr", "describe-images", "--repository-name", $mediaRepositoryName,
    "--image-ids", "imageTag=$gitCommit"
)
$runnerImage = Invoke-AwsJson -Profile $Profile -Region $Region -Arguments @(
    "ecr", "describe-images", "--repository-name", $runnerRepositoryName,
    "--image-ids", "imageTag=$gitCommit"
)
$mediaDigest = [string]$mediaImage.imageDetails[0].imageDigest
$runnerDigest = [string]$runnerImage.imageDetails[0].imageDigest
if ($mediaDigest -notmatch "^sha256:[0-9a-f]{64}$" -or $runnerDigest -notmatch "^sha256:[0-9a-f]{64}$") {
    throw "ECR did not return both immutable image digests."
}

$runtime = [ordered]@{
    deployment_id       = $DeploymentId
    aws_profile         = $Profile
    region              = $Region
    enable_environment  = $true
    media_image_digest  = $mediaDigest
    runner_image_digest = $runnerDigest
    apply_started_at    = $startedText
    expires_at          = $expiresText
    expected_cost_usd   = $ExpectedCostUsd
}
$runtime | ConvertTo-Json | Set-Content -LiteralPath $runtimePath -Encoding utf8NoBOM

Write-Host "Immutable images were built from clean commit $gitCommit and resolved to ECR digests."
Write-Host "The two-hour destruction deadline is $expiresText."
Write-Host "Next: run .\scripts\plan.ps1"
