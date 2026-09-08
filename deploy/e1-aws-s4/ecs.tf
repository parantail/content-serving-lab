resource "aws_cloudwatch_log_group" "media" {
  count = local.environment_count

  name              = "/ecs/${local.resource_name}/media"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "runner" {
  count = local.environment_count

  name              = "/ecs/${local.resource_name}/runner"
  retention_in_days = var.log_retention_days
}

resource "aws_ecs_cluster" "experiment" {
  count = local.environment_count

  name = local.resource_name

  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}

resource "aws_ecs_task_definition" "media" {
  count = local.environment_count

  family                   = "${local.resource_name}-media"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "1024"
  memory                   = "2048"
  execution_role_arn       = aws_iam_role.execution[0].arn
  task_role_arn            = aws_iam_role.media[0].arn
  track_latest             = false

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "media-service"
    image     = "${aws_ecr_repository.media.repository_url}@${local.media_digest}"
    essential = true
    portMappings = [{
      name          = "http"
      containerPort = 8080
      hostPort      = 8080
      protocol      = "tcp"
      appProtocol   = "http"
    }]
    environment = [
      { name = "PORT", value = "8080" },
      { name = "AWS_REGION", value = var.region },
      { name = "AWS_DEFAULT_REGION", value = var.region },
      { name = "STORAGE_BACKEND", value = "s3" },
      { name = "ORIGINAL_BUCKET", value = aws_s3_bucket.original[0].bucket },
      { name = "DERIVATIVE_BUCKET", value = aws_s3_bucket.derivative[0].bucket },
      { name = "COORDINATOR_MODE", value = "process-singleflight" },
      { name = "TRANSFORM_TIMEOUT", value = "60s" },
      { name = "TRANSFORM_CONCURRENCY", value = "4" },
      { name = "E1_EXPERIMENT_MODE", value = "true" },
      { name = "E1_CONTAINER_NAME", value = "media-service" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.media[0].name
        awslogs-region        = var.region
        awslogs-stream-prefix = "media"
      }
    }
  }])

  depends_on = [
    aws_iam_role_policy_attachment.execution,
    aws_iam_role_policy.media,
    aws_s3_object.fixture,
  ]
}

resource "aws_ecs_service" "media" {
  for_each = var.enable_environment ? local.scenarios : {}

  name                               = "${local.resource_name}-${each.key}"
  cluster                            = aws_ecs_cluster.experiment[0].id
  task_definition                    = aws_ecs_task_definition.media[0].arn
  desired_count                      = each.value.desired_count
  launch_type                        = "FARGATE"
  platform_version                   = "LATEST"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = 30
  wait_for_steady_state              = true
  enable_execute_command             = false

  network_configuration {
    assign_public_ip = true
    subnets          = [for subnet in aws_subnet.public : subnet.id]
    security_groups  = [aws_security_group.media[0].id]
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.scenario[each.key].arn
    container_name   = "media-service"
    container_port   = 8080
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  tags = { Scenario = each.value.id }

  depends_on = [aws_lb_listener.scenario, aws_vpc_endpoint.s3]
}

locals {
  calibration_run_id = "calibration-${var.deployment_id}"
  runner_base_command = var.enable_environment ? [
    "run",
    "--run-id", local.calibration_run_id,
    "--calibration",
    "--region", var.region,
    "--container-digest", local.media_digest,
    "--cluster", aws_ecs_cluster.experiment[0].arn,
    "--endpoint-1", "http://${aws_lb.internal[0].dns_name}:8081",
    "--service-1", aws_ecs_service.media["one"].name,
    "--target-group-1", aws_lb_target_group.scenario["one"].arn,
    "--endpoint-2", "http://${aws_lb.internal[0].dns_name}:8082",
    "--service-2", aws_ecs_service.media["two"].name,
    "--target-group-2", aws_lb_target_group.scenario["two"].arn,
    "--endpoint-4", "http://${aws_lb.internal[0].dns_name}:8084",
    "--service-4", aws_ecs_service.media["four"].name,
    "--target-group-4", aws_lb_target_group.scenario["four"].arn,
    "--derivative-bucket", aws_s3_bucket.derivative[0].bucket,
    "--result-bucket", aws_s3_bucket.result[0].bucket,
    "--result-prefix", var.result_prefix,
    "--results-root", "/tmp/results-aws-s4",
  ] : []
}

resource "aws_ecs_task_definition" "runner" {
  count = local.environment_count

  family                   = "${local.resource_name}-runner"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "1024"
  memory                   = "2048"
  execution_role_arn       = aws_iam_role.execution[0].arn
  task_role_arn            = aws_iam_role.runner[0].arn
  track_latest             = false

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "load-generator"
    image     = "${aws_ecr_repository.runner.repository_url}@${local.runner_digest}"
    essential = true
    command   = local.runner_base_command
    environment = [
      { name = "AWS_REGION", value = var.region },
      { name = "AWS_DEFAULT_REGION", value = var.region },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.runner[0].name
        awslogs-region        = var.region
        awslogs-stream-prefix = "runner"
      }
    }
  }])

  depends_on = [
    aws_ecs_service.media,
    aws_iam_role_policy_attachment.execution,
    aws_iam_role_policy.runner,
  ]
}
