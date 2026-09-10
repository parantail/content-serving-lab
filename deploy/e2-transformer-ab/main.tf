terraform {
  required_version = ">= 1.15.0, < 1.17.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.53.0"
    }
  }
}

provider "aws" {
  region  = var.region
  profile = var.aws_profile
  default_tags { tags = local.tags }
}

variable "aws_profile" {
  type    = string
  default = "content-serving-lab-sandbox"
}
variable "region" {
  type    = string
  default = "ap-northeast-2"
  validation {
    condition     = var.region == "ap-northeast-2"
    error_message = "E2 runs in Seoul."
  }
}
variable "deployment_id" {
  type = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{7,15}$", var.deployment_id))
    error_message = "Use a unique 8-16 character deployment id."
  }
}
variable "expires_at" { type = string }
variable "image_digest" {
  type    = string
  default = ""
  validation {
    condition     = var.image_digest == "" || can(regex("^sha256:[0-9a-f]{64}$", var.image_digest))
    error_message = "Use a content-addressed image digest."
  }
}

locals {
  name = "content-serving-e2-${var.deployment_id}"
  tags = {
    Project      = "content-serving-lab"
    Experiment   = "e2-transformer-ab"
    DeploymentId = var.deployment_id
    ExpiresAt    = var.expires_at
  }
}

resource "aws_ecr_repository" "runner" {
  name                 = local.name
  image_tag_mutability = "IMMUTABLE"
  force_delete         = true
  image_scanning_configuration { scan_on_push = false }
}

resource "aws_vpc" "experiment" {
  cidr_block           = "10.82.0.0/24"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }
}
resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.experiment.id
  cidr_block        = "10.82.0.0/26"
  availability_zone = "${var.region}a"
  tags              = { Name = local.name }
}
resource "aws_internet_gateway" "experiment" {
  vpc_id = aws_vpc.experiment.id
}
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.experiment.id
}
resource "aws_route" "internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.experiment.id
}
resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.experiment.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.public.id]
}
resource "aws_security_group" "runner" {
  name        = local.name
  description = "E2 one-shot runner; no inbound"
  vpc_id      = aws_vpc.experiment.id
}
resource "aws_vpc_security_group_egress_rule" "https" {
  security_group_id = aws_security_group.runner.id
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}
resource "aws_s3_bucket" "results" {
  bucket        = local.name
  force_destroy = true
}
resource "aws_s3_bucket_public_access_block" "results" {
  bucket                  = aws_s3_bucket.results.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "results" {
  bucket = aws_s3_bucket.results.id
  rule { object_ownership = "BucketOwnerEnforced" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "results" {
  bucket = aws_s3_bucket.results.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_cloudwatch_log_group" "runner" {
  name              = "/content-serving/e2/${var.deployment_id}"
  retention_in_days = 1
}
resource "aws_ecs_cluster" "experiment" { name = local.name }

locals {
  assume_task = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ecs-tasks.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role" "execution" {
  name               = "${local.name}-execution"
  assume_role_policy = local.assume_task
}
resource "aws_iam_role_policy" "execution" {
  name = "pull-and-log"
  role = aws_iam_role.execution.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = "ecr:GetAuthorizationToken", Resource = "*" },
    { Effect = "Allow", Action = ["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"], Resource = aws_ecr_repository.runner.arn },
    { Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents"], Resource = "${aws_cloudwatch_log_group.runner.arn}:*" }
  ] })
}
resource "aws_iam_role" "runner" {
  name               = "${local.name}-runner"
  assume_role_policy = local.assume_task
}
resource "aws_iam_role_policy" "results" {
  name = "write-results"
  role = aws_iam_role.runner.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = "s3:PutObject", Resource = "${aws_s3_bucket.results.arn}/*" }
  ] })
}
resource "aws_ecs_task_definition" "runner" {
  count                    = var.image_digest == "" ? 0 : 1
  family                   = local.name
  cpu                      = "1024"
  memory                   = "2048"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.runner.arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  container_definitions = jsonencode([{
    name        = "runner"
    image       = "${aws_ecr_repository.runner.repository_url}@${var.image_digest}"
    essential   = true
    cpu         = 1024
    memory      = 2048
    user        = "10001:10001"
    stopTimeout = 30
    environment = [{ name = "E2_RESULTS_BUCKET", value = aws_s3_bucket.results.id }, { name = "AWS_REGION", value = var.region }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.runner.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "e2"
      }
    }
  }])
  depends_on = [aws_iam_role_policy.execution, aws_iam_role_policy.results]
}

output "repository_url" { value = aws_ecr_repository.runner.repository_url }
output "run_configuration" {
  value = {
    cluster         = aws_ecs_cluster.experiment.arn
    family          = local.name
    task_definition = try(aws_ecs_task_definition.runner[0].arn, null)
    result_bucket   = aws_s3_bucket.results.id
    subnet          = aws_subnet.public.id
    security_group  = aws_security_group.runner.id
    log_group       = aws_cloudwatch_log_group.runner.name
    vpc             = aws_vpc.experiment.id
  }
}
