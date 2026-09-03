resource "aws_lb" "internal" {
  count = local.environment_count

  name                       = substr(local.resource_name, 0, 32)
  internal                   = true
  load_balancer_type         = "application"
  security_groups            = [aws_security_group.alb[0].id]
  subnets                    = [for subnet in aws_subnet.public : subnet.id]
  drop_invalid_header_fields = true
  enable_deletion_protection = false
  idle_timeout               = 120

  tags = { Name = local.resource_name }
}

resource "aws_lb_target_group" "scenario" {
  for_each = var.enable_environment ? local.scenarios : {}

  # Keep the scenario suffix inside the 32-character ELB name limit. Truncating
  # the complete string could otherwise give all three target groups one name.
  name                          = "${substr(local.resource_name, 0, 26)}-${each.key}"
  port                          = 8080
  protocol                      = "HTTP"
  target_type                   = "ip"
  vpc_id                        = aws_vpc.experiment[0].id
  deregistration_delay          = 30
  load_balancing_algorithm_type = "round_robin"

  stickiness {
    type    = "lb_cookie"
    enabled = false
  }

  health_check {
    enabled             = true
    path                = "/health/ready"
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 10
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 2
  }

  tags = {
    Name     = "${local.resource_name}-${each.key}"
    Scenario = each.value.id
  }
}

resource "aws_lb_listener" "scenario" {
  for_each = var.enable_environment ? local.scenarios : {}

  load_balancer_arn = aws_lb.internal[0].arn
  port              = each.value.listener_port
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.scenario[each.key].arn
  }

  tags = { Scenario = each.value.id }
}
