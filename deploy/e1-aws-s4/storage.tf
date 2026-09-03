locals {
  original_bucket_name   = "${local.bucket_prefix}-original"
  derivative_bucket_name = "${local.bucket_prefix}-derivative"
  result_bucket_name     = "${local.bucket_prefix}-result"
  s3_endpoint_policy = var.enable_environment ? jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "ExperimentBucketsOnly"
      Effect    = "Allow"
      Principal = "*"
      Action    = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
      Resource = [
        aws_s3_bucket.original[0].arn,
        "${aws_s3_bucket.original[0].arn}/*",
        aws_s3_bucket.derivative[0].arn,
        "${aws_s3_bucket.derivative[0].arn}/*",
        aws_s3_bucket.result[0].arn,
        "${aws_s3_bucket.result[0].arn}/*",
      ]
    }]
  }) : null
}

resource "aws_s3_bucket" "original" {
  count = local.environment_count

  bucket        = local.original_bucket_name
  force_destroy = true
}

resource "aws_s3_bucket" "derivative" {
  count = local.environment_count

  bucket        = local.derivative_bucket_name
  force_destroy = true
}

resource "aws_s3_bucket" "result" {
  count = local.environment_count

  bucket        = local.result_bucket_name
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "original" {
  count = local.environment_count

  bucket                  = aws_s3_bucket.original[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_public_access_block" "derivative" {
  count = local.environment_count

  bucket                  = aws_s3_bucket.derivative[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_public_access_block" "result" {
  count = local.environment_count

  bucket                  = aws_s3_bucket.result[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "original" {
  count = local.environment_count

  bucket = aws_s3_bucket.original[0].id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_ownership_controls" "derivative" {
  count = local.environment_count

  bucket = aws_s3_bucket.derivative[0].id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_ownership_controls" "result" {
  count = local.environment_count

  bucket = aws_s3_bucket.result[0].id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "original" {
  count = local.environment_count

  bucket = aws_s3_bucket.original[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "derivative" {
  count = local.environment_count

  bucket = aws_s3_bucket.derivative[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "result" {
  count = local.environment_count

  bucket = aws_s3_bucket.result[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "original" {
  count = local.environment_count

  bucket = aws_s3_bucket.original[0].id
  rule {
    id     = "expire-disposable-data"
    status = "Enabled"
    filter {}
    expiration { days = 1 }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "derivative" {
  count = local.environment_count

  bucket = aws_s3_bucket.derivative[0].id
  rule {
    id     = "expire-disposable-data"
    status = "Enabled"
    filter {}
    expiration { days = 1 }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "result" {
  count = local.environment_count

  bucket = aws_s3_bucket.result[0].id
  rule {
    id     = "expire-disposable-data"
    status = "Enabled"
    filter {}
    expiration { days = 1 }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}

resource "aws_s3_bucket_policy" "original" {
  count = local.environment_count

  bucket = aws_s3_bucket.original[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.original[0].arn, "${aws_s3_bucket.original[0].arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.original]
}

resource "aws_s3_bucket_policy" "derivative" {
  count = local.environment_count

  bucket = aws_s3_bucket.derivative[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [aws_s3_bucket.derivative[0].arn, "${aws_s3_bucket.derivative[0].arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid       = "DenyPutWithoutIfNoneMatch"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.derivative[0].arn}/derivatives/*"
        Condition = { Null = { "s3:if-none-match" = "true" } }
      },
      {
        Sid       = "DenyPutWithWrongIfNoneMatch"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.derivative[0].arn}/derivatives/*"
        Condition = { StringNotEquals = { "s3:if-none-match" = "*" } }
      },
    ]
  })

  depends_on = [aws_s3_bucket_public_access_block.derivative]
}

resource "aws_s3_bucket_policy" "result" {
  count = local.environment_count

  bucket = aws_s3_bucket.result[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.result[0].arn, "${aws_s3_bucket.result[0].arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.result]
}

resource "aws_s3_object" "fixture" {
  count = local.environment_count

  bucket       = aws_s3_bucket.original[0].id
  key          = "originals/${var.source_hash}"
  source       = local.fixture_path
  source_hash  = filemd5(local.fixture_path)
  content_type = "image/jpeg"

  depends_on = [
    aws_s3_bucket_ownership_controls.original,
    aws_s3_bucket_server_side_encryption_configuration.original,
  ]
}
