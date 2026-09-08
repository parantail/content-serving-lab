mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:user/test-only"
      id         = "123456789012"
    }
  }

  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/test-only"
    }
  }

  mock_resource "aws_lb" {
    defaults = {
      arn      = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/test-only/0000000000000000"
      dns_name = "internal-test-only.ap-northeast-2.elb.amazonaws.com"
    }
  }

  mock_resource "aws_lb_target_group" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/test-only/0000000000000000"
    }
  }

  mock_resource "aws_ecs_cluster" {
    defaults = {
      arn = "arn:aws:ecs:ap-northeast-2:123456789012:cluster/test-only"
      id  = "arn:aws:ecs:ap-northeast-2:123456789012:cluster/test-only"
    }
  }

  mock_resource "aws_ecs_task_definition" {
    defaults = {
      arn = "arn:aws:ecs:ap-northeast-2:123456789012:task-definition/test-only:1"
    }
  }
}

run "bootstrap_creates_only_ecr" {
  command = plan

  variables {
    deployment_id = "deadbeef1234"
  }

  assert {
    condition     = aws_ecr_repository.media.image_tag_mutability == "IMMUTABLE" && aws_ecr_repository.runner.image_tag_mutability == "IMMUTABLE"
    error_message = "Both bootstrap repositories must enforce immutable tags."
  }

  assert {
    condition     = length(aws_vpc.experiment) == 0 && length(aws_ecs_cluster.experiment) == 0 && length(aws_s3_bucket.original) == 0
    error_message = "The bootstrap plan must not create the timed environment."
  }
}

run "full_environment_matches_fixed_contract" {
  command = apply

  assert {
    condition     = contains(local.runner_base_command, "--calibration=true") && local.workload_run_id == "calibration-deadbeef1234"
    error_message = "Default execution must remain calibration."
  }

  assert {
    condition = length([for statement in jsondecode(aws_vpc_endpoint.s3[0].policy).Statement : statement if
      statement.Sid == "ECRImageLayers" &&
      statement.Effect == "Allow" && statement.Principal == "*" &&
      statement.Action == ["s3:GetObject"] &&
      statement.Resource == ["arn:aws:s3:::prod-ap-northeast-2-starport-layer-bucket/*"]
    ]) == 1
    error_message = "The S3 endpoint must allow read-only access to this region's ECR image layers."
  }

  variables {
    deployment_id       = "deadbeef1234"
    enable_environment  = true
    media_image_digest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
    runner_image_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
    apply_started_at    = "2026-09-03T00:00:00Z"
    expires_at          = "2026-09-03T02:00:00Z"
    expected_cost_usd   = 5.00
  }

  assert {
    condition = (
      aws_ecs_service.media["one"].desired_count == 1 &&
      aws_ecs_service.media["two"].desired_count == 2 &&
      aws_ecs_service.media["four"].desired_count == 4
    )
    error_message = "The three fixed services must keep desired counts 1, 2, and 4."
  }

  assert {
    condition = (
      aws_ecs_task_definition.media[0].cpu == "1024" &&
      aws_ecs_task_definition.media[0].memory == "2048" &&
      aws_ecs_task_definition.runner[0].cpu == "1024" &&
      aws_ecs_task_definition.runner[0].memory == "2048"
    )
    error_message = "Media and runner tasks must remain fixed at 1 vCPU and 2 GiB."
  }

  assert {
    condition = (
      aws_lb.internal[0].internal &&
      length(aws_lb_listener.scenario) == 3 &&
      length(distinct([for group in aws_lb_target_group.scenario : group.name])) == 3 &&
      alltrue([for group in aws_lb_target_group.scenario : group.load_balancing_algorithm_type == "round_robin" && !group.stickiness[0].enabled])
    )
    error_message = "The internal ALB must expose three distinct, non-sticky round-robin target groups."
  }

  assert {
    condition = (
      length(aws_subnet.public) == 2 &&
      length(aws_vpc_endpoint.s3) == 1 &&
      alltrue([for service in aws_ecs_service.media : service.network_configuration[0].assign_public_ip])
    )
    error_message = "The NAT-free topology requires two public subnets, public task IPs, and one S3 gateway endpoint."
  }

  assert {
    condition = (
      length(aws_s3_bucket.original) == 1 &&
      length(aws_s3_bucket.derivative) == 1 &&
      length(aws_s3_bucket.result) == 1 &&
      aws_s3_bucket_public_access_block.original[0].block_public_acls &&
      aws_s3_bucket_public_access_block.original[0].block_public_policy &&
      aws_s3_bucket_public_access_block.original[0].ignore_public_acls &&
      aws_s3_bucket_public_access_block.original[0].restrict_public_buckets
    )
    error_message = "All three fixed buckets and bucket-level public access protection are required."
  }

  assert {
    condition = (
      length([for statement in jsondecode(aws_s3_bucket_policy.derivative[0].policy).Statement : statement if statement.Sid == "DenyPutWithoutIfNoneMatch"]) == 1 &&
      length([for statement in jsondecode(aws_s3_bucket_policy.derivative[0].policy).Statement : statement if statement.Sid == "DenyPutWithWrongIfNoneMatch"]) == 1
    )
    error_message = "The derivative bucket must deny unconditional or incorrectly conditional writes."
  }

  assert {
    condition = (
      aws_iam_role.execution[0].name != aws_iam_role.media[0].name &&
      aws_iam_role.execution[0].name != aws_iam_role.runner[0].name &&
      aws_iam_role.media[0].name != aws_iam_role.runner[0].name
    )
    error_message = "Execution, media, and runner IAM roles must remain distinct."
  }

  assert {
    condition = (
      length([for statement in jsondecode(aws_iam_role_policy.media[0].policy).Statement : statement if statement.Action == "s3:ListBucket" && statement.Resource == aws_s3_bucket.derivative[0].arn]) == 1 &&
      length([for statement in jsondecode(aws_iam_role_policy.runner[0].policy).Statement : statement if statement.Action == "s3:ListBucket" && statement.Resource == aws_s3_bucket.derivative[0].arn]) == 1
    )
    error_message = "Media GET and runner HEAD must both be able to distinguish an exact derivative miss from AccessDenied."
  }

  assert {
    condition = (
      length(aws_vpc_security_group_ingress_rule.media_from_alb) == 1 &&
      length(aws_vpc_security_group_ingress_rule.alb_from_runner) == 3 &&
      length(aws_vpc_security_group_egress_rule.runner_to_alb) == 3
    )
    error_message = "Only the runner-to-ALB and ALB-to-media application paths may be opened."
  }

  assert {
    condition     = aws_cloudwatch_log_group.media[0].retention_in_days == 1 && aws_cloudwatch_log_group.runner[0].retention_in_days == 1
    error_message = "Disposable experiment logs must expire after one day."
  }
}

run "retained_command_contract" {
  command = apply
  variables {
    deployment_id       = "deadbeef1234"
    enable_environment  = true
    run_mode            = "retained"
    media_image_digest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
    runner_image_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
    apply_started_at    = "2026-09-03T00:00:00Z"
    expires_at          = "2026-09-03T02:00:00Z"
    expected_cost_usd   = 3
  }
  assert {
    condition     = local.workload_run_id == "retained-deadbeef1234" && contains(local.runner_base_command, "--calibration=false") && local.runner_base_command[index(local.runner_base_command, "--start-skew-limit") + 1] == "50ms" && local.runner_base_command[index(local.runner_base_command, "--repetitions") + 1] == "10"
    error_message = "Retained mode must fix its separate run ID, repetitions and skew."
  }
  assert {
    condition     = output.run_task_configuration.run_mode == "retained" && output.run_task_configuration.run_id == local.workload_run_id && output.run_task_configuration.media_digest == var.media_image_digest
    error_message = "Run-task mode and diagnostic image gate must match Terraform."
  }
}
