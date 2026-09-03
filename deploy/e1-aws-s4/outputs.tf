output "ecr_repository_urls" {
  description = "Repository URLs used by the image publishing helper. Account-derived, so suppress them in normal CLI output."
  value = {
    media  = aws_ecr_repository.media.repository_url
    runner = aws_ecr_repository.runner.repository_url
  }
  sensitive = true
}

output "experiment_contract" {
  description = "Non-secret fixed topology and workload contract."
  value = {
    region              = var.region
    scenarios           = local.scenarios
    source_hash         = var.source_hash
    derivative_key      = "derivatives/${local.derivative_key}.webp"
    canonical_spec      = var.canonical_spec
    maximum_lifetime    = "2h"
    expected_cost_usd   = var.expected_cost_usd
    environment_enabled = var.enable_environment
  }
}

output "run_task_configuration" {
  description = "Values consumed by run-task.ps1 after the full environment is applied."
  value = var.enable_environment ? {
    cluster         = aws_ecs_cluster.experiment[0].arn
    task_definition = aws_ecs_task_definition.runner[0].arn
    runner_family   = aws_ecs_task_definition.runner[0].family
    subnets         = [for subnet in aws_subnet.public : subnet.id]
    security_group  = aws_security_group.runner[0].id
    result_bucket   = aws_s3_bucket.result[0].bucket
    result_prefix   = var.result_prefix
    expires_at      = var.expires_at
  } : null
  sensitive = true
}
