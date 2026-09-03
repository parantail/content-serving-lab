resource "aws_ecr_repository" "media" {
  name                 = "${var.name_prefix}-${var.deployment_id}/media"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = true

  encryption_configuration {
    encryption_type = "AES256"
  }

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_repository" "runner" {
  name                 = "${var.name_prefix}-${var.deployment_id}/runner"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = true

  encryption_configuration {
    encryption_type = "AES256"
  }

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "media" {
  repository = aws_ecr_repository.media.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Retain only the two most recent experiment images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 2
      }
      action = { type = "expire" }
    }]
  })
}

resource "aws_ecr_lifecycle_policy" "runner" {
  repository = aws_ecr_repository.runner.name
  policy     = aws_ecr_lifecycle_policy.media.policy
}
