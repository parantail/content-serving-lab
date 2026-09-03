locals {
  ecs_task_trust_policy = var.enable_environment ? jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = data.aws_caller_identity.current.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:aws:ecs:${var.region}:${data.aws_caller_identity.current.account_id}:*" }
      }
    }]
  }) : null
}

data "aws_caller_identity" "current" {
}

resource "aws_iam_role" "execution" {
  count = local.environment_count

  name               = "${local.resource_name}-execution"
  assume_role_policy = local.ecs_task_trust_policy
}

resource "aws_iam_role_policy_attachment" "execution" {
  count = local.environment_count

  role       = aws_iam_role.execution[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role" "media" {
  count = local.environment_count

  name               = "${local.resource_name}-media"
  assume_role_policy = local.ecs_task_trust_policy
}

resource "aws_iam_role_policy" "media" {
  count = local.environment_count

  name = "s3-exact-experiment-objects"
  role = aws_iam_role.media[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListDerivativeBucketForExactMissClassification"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = aws_s3_bucket.derivative[0].arn
      },
      {
        Sid      = "ReadFixedOriginal"
        Effect   = "Allow"
        Action   = "s3:GetObject"
        Resource = "${aws_s3_bucket.original[0].arn}/originals/${var.source_hash}"
      },
      {
        Sid      = "ReadAndConditionallyPublishFixedDerivative"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject"]
        Resource = "${aws_s3_bucket.derivative[0].arn}/derivatives/${local.derivative_key}.webp"
      },
    ]
  })
}

resource "aws_iam_role" "runner" {
  count = local.environment_count

  name               = "${local.resource_name}-runner"
  assume_role_policy = local.ecs_task_trust_policy
}

resource "aws_iam_role_policy" "runner" {
  count = local.environment_count

  name = "workload-control-and-results"
  role = aws_iam_role.runner[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListDerivativeBucketForExactHeadMiss"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = aws_s3_bucket.derivative[0].arn
      },
      {
        Sid      = "ResetAndVerifyFixedDerivative"
        Effect   = "Allow"
        Action   = ["s3:DeleteObject", "s3:GetObject"]
        Resource = "${aws_s3_bucket.derivative[0].arn}/derivatives/${local.derivative_key}.webp"
      },
      {
        Sid      = "UploadRunResults"
        Effect   = "Allow"
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.result[0].arn}/${var.result_prefix}/*"
      },
      {
        Sid      = "InspectExperimentServicesAndTasks"
        Effect   = "Allow"
        Action   = ["ecs:DescribeServices", "ecs:DescribeTasks", "ecs:ListTasks"]
        Resource = "*"
        Condition = {
          ArnEquals = { "ecs:cluster" = aws_ecs_cluster.experiment[0].arn }
        }
      },
      {
        Sid    = "InspectExperimentTargetGroups"
        Effect = "Allow"
        Action = ["elasticloadbalancing:DescribeTargetGroupAttributes", "elasticloadbalancing:DescribeTargetHealth"]
        # ELBv2 Describe actions do not support resource-level IAM scoping.
        Resource = "*"
      },
    ]
  })
}
