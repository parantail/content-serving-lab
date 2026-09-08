variable "region" {
  description = "Fixed AWS Region for E1 AWS S4."
  type        = string
  default     = "ap-northeast-2"

  validation {
    condition     = var.region == "ap-northeast-2"
    error_message = "E1 AWS S4 is fixed to ap-northeast-2."
  }
}

variable "aws_profile" {
  description = "Optional local AWS CLI profile. ECS tasks use task roles instead."
  type        = string
  default     = null
  nullable    = true
}

variable "name_prefix" {
  description = "Lowercase prefix used for named resources."
  type        = string
  default     = "content-serving-e1-s4"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,22}$", var.name_prefix))
    error_message = "name_prefix must be 3-23 lowercase letters, digits, or hyphens and start with a letter."
  }
}

variable "deployment_id" {
  description = "Globally distinguishing lowercase ID, such as the first 12 characters of the clean commit SHA."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{7,15}$", var.deployment_id))
    error_message = "deployment_id must be 8-16 lowercase letters, digits, or hyphens."
  }
}

variable "enable_environment" {
  description = "False creates only immutable ECR repositories; true creates the timed experiment environment."
  type        = bool
  default     = false
}

variable "media_image_digest" {
  description = "Digest pushed to the Terraform-managed media ECR repository. Required when enable_environment is true."
  type        = string
  default     = null
  nullable    = true

  validation {
    condition     = var.media_image_digest == null || can(regex("^sha256:[0-9a-f]{64}$", var.media_image_digest))
    error_message = "media_image_digest must be a sha256 digest."
  }
}

variable "runner_image_digest" {
  description = "Digest pushed to the Terraform-managed load-generator ECR repository. Required when enable_environment is true."
  type        = string
  default     = null
  nullable    = true

  validation {
    condition     = var.runner_image_digest == null || can(regex("^sha256:[0-9a-f]{64}$", var.runner_image_digest))
    error_message = "runner_image_digest must be a sha256 digest."
  }
}

variable "apply_started_at" {
  description = "RFC3339 UTC time captured immediately before the full apply. Required when enable_environment is true."
  type        = string
  default     = null
  nullable    = true

  validation {
    condition     = var.apply_started_at == null || can(timecmp(var.apply_started_at, var.apply_started_at))
    error_message = "apply_started_at must be RFC3339."
  }
}

variable "expires_at" {
  description = "RFC3339 UTC hard deadline, no more than two hours after apply_started_at. Required for the full environment."
  type        = string
  default     = null
  nullable    = true

  validation {
    condition     = var.expires_at == null || can(timecmp(var.expires_at, var.expires_at))
    error_message = "expires_at must be RFC3339."
  }
}

variable "expected_cost_usd" {
  description = "Operator-reviewed maximum expected cost for this run. Required for the full environment and must remain below the US$10 budget."
  type        = number
  default     = null
  nullable    = true

  validation {
    condition     = var.expected_cost_usd == null || (var.expected_cost_usd > 0 && var.expected_cost_usd < 10)
    error_message = "expected_cost_usd must be greater than zero and lower than 10."
  }
}

variable "availability_zones" {
  description = "Two available ap-northeast-2 zones checked by preflight."
  type        = list(string)
  default     = ["ap-northeast-2a", "ap-northeast-2c"]

  validation {
    condition     = length(var.availability_zones) == 2 && length(distinct(var.availability_zones)) == 2 && alltrue([for zone in var.availability_zones : startswith(zone, "ap-northeast-2")])
    error_message = "availability_zones must contain two distinct ap-northeast-2 zones."
  }
}

variable "vpc_cidr" {
  description = "Dedicated experiment VPC CIDR."
  type        = string
  default     = "10.42.0.0/16"
}

variable "source_hash" {
  description = "SHA-256 of the retained landscape fixture uploaded to Original S3."
  type        = string
  default     = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.source_hash))
    error_message = "source_hash must be a lowercase SHA-256."
  }
}

variable "canonical_spec" {
  description = "Canonical transform used by the fixed AWS S4 workload."
  type        = string
  default     = "width=640,height=640,fit=cover,quality=80,format=webp"

  validation {
    condition     = var.canonical_spec == "width=640,height=640,fit=cover,quality=80,format=webp"
    error_message = "A4 supports only the precommitted 640x640 WebP transform."
  }
}

variable "run_mode" {
  description = "Explicit calibration or retained workload; defaults to calibration."
  type        = string
  default     = "calibration"
  validation {
    condition     = contains(["calibration", "retained"], var.run_mode)
    error_message = "run_mode must be calibration or retained."
  }
}

variable "result_prefix" {
  description = "Result bucket object prefix used by e1-aws-s4."
  type        = string
  default     = "experiments/e1-cache-stampede/results-aws-s4"
}

variable "log_retention_days" {
  description = "Short CloudWatch Logs retention for the disposable experiment."
  type        = number
  default     = 1

  validation {
    condition     = var.log_retention_days == 1
    error_message = "E1 AWS S4 keeps CloudWatch logs for one day only."
  }
}
