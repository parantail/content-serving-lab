resource "aws_vpc" "experiment" {
  count = local.environment_count

  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = local.resource_name }
}

resource "aws_internet_gateway" "experiment" {
  count = local.environment_count

  vpc_id = aws_vpc.experiment[0].id
  tags   = { Name = local.resource_name }
}

resource "aws_subnet" "public" {
  for_each = var.enable_environment ? local.subnet_cidrs : {}

  vpc_id                  = aws_vpc.experiment[0].id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false

  tags = { Name = "${local.resource_name}-${substr(each.key, length(each.key) - 1, 1)}" }
}

resource "aws_route_table" "public" {
  count = local.environment_count

  vpc_id = aws_vpc.experiment[0].id
  tags   = { Name = "${local.resource_name}-public" }
}

resource "aws_route" "internet" {
  count = local.environment_count

  route_table_id         = aws_route_table.public[0].id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.experiment[0].id
}

resource "aws_route_table_association" "public" {
  for_each = aws_subnet.public

  subnet_id      = each.value.id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_vpc_endpoint" "s3" {
  count = local.environment_count

  vpc_id            = aws_vpc.experiment[0].id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.public[0].id]
  policy            = local.s3_endpoint_policy

  tags = { Name = "${local.resource_name}-s3" }
}

resource "aws_security_group" "alb" {
  count = local.environment_count

  name        = "${local.resource_name}-alb"
  description = "Internal ALB accepts only the E1 load generator"
  vpc_id      = aws_vpc.experiment[0].id

  tags = { Name = "${local.resource_name}-alb" }
}

resource "aws_security_group" "media" {
  count = local.environment_count

  name        = "${local.resource_name}-media"
  description = "Media tasks accept only traffic from the internal ALB"
  vpc_id      = aws_vpc.experiment[0].id

  tags = { Name = "${local.resource_name}-media" }
}

resource "aws_security_group" "runner" {
  count = local.environment_count

  name        = "${local.resource_name}-runner"
  description = "One-shot load generator with no inbound access"
  vpc_id      = aws_vpc.experiment[0].id

  tags = { Name = "${local.resource_name}-runner" }
}

resource "aws_vpc_security_group_ingress_rule" "alb_from_runner" {
  for_each = var.enable_environment ? local.scenarios : {}

  security_group_id            = aws_security_group.alb[0].id
  referenced_security_group_id = aws_security_group.runner[0].id
  ip_protocol                  = "tcp"
  from_port                    = each.value.listener_port
  to_port                      = each.value.listener_port
  description                  = each.value.id
}

resource "aws_vpc_security_group_egress_rule" "alb_to_media" {
  count = local.environment_count

  security_group_id            = aws_security_group.alb[0].id
  referenced_security_group_id = aws_security_group.media[0].id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  description                  = "ALB to media tasks"
}

resource "aws_vpc_security_group_ingress_rule" "media_from_alb" {
  count = local.environment_count

  security_group_id            = aws_security_group.media[0].id
  referenced_security_group_id = aws_security_group.alb[0].id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  description                  = "Media service port from ALB only"
}

resource "aws_vpc_security_group_egress_rule" "media_https" {
  count = local.environment_count

  security_group_id = aws_security_group.media[0].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  description       = "AWS APIs, ECR and logs"
}

resource "aws_vpc_security_group_egress_rule" "runner_https" {
  count = local.environment_count

  security_group_id = aws_security_group.runner[0].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  description       = "ECS, ELB and S3 APIs"
}

resource "aws_vpc_security_group_egress_rule" "runner_to_alb" {
  for_each = var.enable_environment ? local.scenarios : {}

  security_group_id            = aws_security_group.runner[0].id
  referenced_security_group_id = aws_security_group.alb[0].id
  ip_protocol                  = "tcp"
  from_port                    = each.value.listener_port
  to_port                      = each.value.listener_port
  description                  = each.value.id
}

resource "aws_vpc_security_group_egress_rule" "dns_udp" {
  for_each = var.enable_environment ? toset(["media", "runner"]) : toset([])

  security_group_id = each.key == "media" ? aws_security_group.media[0].id : aws_security_group.runner[0].id
  cidr_ipv4         = var.vpc_cidr
  ip_protocol       = "udp"
  from_port         = 53
  to_port           = 53
  description       = "VPC DNS"
}

resource "aws_vpc_security_group_egress_rule" "dns_tcp" {
  for_each = var.enable_environment ? toset(["media", "runner"]) : toset([])

  security_group_id = each.key == "media" ? aws_security_group.media[0].id : aws_security_group.runner[0].id
  cidr_ipv4         = var.vpc_cidr
  ip_protocol       = "tcp"
  from_port         = 53
  to_port           = 53
  description       = "VPC DNS fallback"
}
