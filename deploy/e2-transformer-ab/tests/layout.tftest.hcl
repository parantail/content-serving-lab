mock_provider "aws" {
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/e2-mock" }
  }
}

variables {
  deployment_id = "test-e2-layout"
  expires_at    = "2026-09-10T03:00:00Z"
  image_digest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

run "one_shot_contract" {
  command = apply
  assert {
    condition     = aws_ecs_task_definition.runner[0].cpu == "1024" && aws_ecs_task_definition.runner[0].memory == "2048"
    error_message = "E2 must keep 1 vCPU and 2 GiB."
  }
  assert {
    condition     = length(jsondecode(aws_ecs_task_definition.runner[0].container_definitions)) == 1
    error_message = "Run a single supervisor container."
  }
  assert {
    condition     = aws_ecr_repository.runner.image_tag_mutability == "IMMUTABLE"
    error_message = "The E2 checkpoint image must be immutable."
  }
  assert {
    condition     = aws_vpc_endpoint.s3.vpc_endpoint_type == "Gateway"
    error_message = "Do not add paid interface endpoints to E2."
  }
  assert {
    condition     = aws_s3_bucket_public_access_block.results.block_public_acls && aws_s3_bucket_public_access_block.results.block_public_policy && aws_s3_bucket_public_access_block.results.ignore_public_acls && aws_s3_bucket_public_access_block.results.restrict_public_buckets
    error_message = "Results must remain private."
  }
}
