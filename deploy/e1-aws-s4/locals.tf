locals {
  environment_count = var.enable_environment ? 1 : 0
  resource_name     = substr("${var.name_prefix}-${var.deployment_id}", 0, 32)
  # S3 bucket names are global. A one-way account namespace prevents two
  # readers of the same public checkpoint from claiming the same names.
  account_namespace = substr(sha256(data.aws_caller_identity.current.account_id), 0, 8)
  bucket_prefix     = "${substr(replace("${var.name_prefix}-${var.deployment_id}", "_", "-"), 0, 41)}-${local.account_namespace}"
  derivative_key    = sha256("${lower(var.source_hash)}\n${var.canonical_spec}")
  fixture_path      = "${path.module}/../../experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg"
  media_digest      = coalesce(var.media_image_digest, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
  runner_digest     = coalesce(var.runner_image_digest, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
  scenarios = {
    one = {
      id            = "S4-AWS-1-CONTROL"
      desired_count = 1
      listener_port = 8081
    }
    two = {
      id            = "S4-AWS-2"
      desired_count = 2
      listener_port = 8082
    }
    four = {
      id            = "S4-AWS-4"
      desired_count = 4
      listener_port = 8084
    }
  }
  subnet_cidrs = zipmap(var.availability_zones, ["10.42.0.0/24", "10.42.1.0/24"])
  common_tags = {
    Project    = "content-serving-lab"
    Experiment = "e1-aws-s4"
    ManagedBy  = "terraform"
    ExpiresAt  = coalesce(var.expires_at, "bootstrap-only")
  }
}

resource "terraform_data" "deadline_guard" {
  count = local.environment_count

  input = {
    apply_started_at  = var.apply_started_at
    expires_at        = var.expires_at
    expected_cost_usd = var.expected_cost_usd
    media_digest      = var.media_image_digest
    runner_digest     = var.runner_image_digest
  }

  lifecycle {
    precondition {
      condition     = var.apply_started_at != null && var.expires_at != null && var.expected_cost_usd != null && var.media_image_digest != null && var.runner_image_digest != null
      error_message = "The full environment requires timestamps, reviewed cost, and both image digests."
    }
    precondition {
      condition     = var.apply_started_at == null || var.expires_at == null ? false : timecmp(var.expires_at, var.apply_started_at) > 0 && timecmp(var.expires_at, timeadd(var.apply_started_at, "2h")) <= 0
      error_message = "expires_at must be after apply_started_at and no more than two hours later."
    }
  }
}
