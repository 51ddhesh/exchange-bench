variable "project" {}
variable "vpc_id" {}
variable "public_subnet_ids" {}

resource "aws_security_group" "alb" {
  name        = "${var.project}-alb-sg"
  description = "ALB - allow HTTP from internet"
  vpc_id      = var.vpc_id

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = { Name = "${var.project}-alb-sg" }
}

resource "aws_lb" "main" {
  name               = "${var.project}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnet_ids
  tags               = { Name = "${var.project}-alb" }
}

resource "aws_lb_target_group" "api" {
  name        = "${var.project}-api-tg"
  port        = 8081
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "instance"

  health_check {
    path                = "/health"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
  tags = { Name = "${var.project}-api-tg" }
}

resource "aws_lb_target_group" "leaderboard" {
  name        = "${var.project}-lb-tg"
  port        = 8080
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  health_check {
    path                = "/"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
  tags = { Name = "${var.project}-lb-tg" }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"

  # Default action: route to leaderboard
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.leaderboard.arn
  }
}

resource "aws_lb_listener_rule" "api" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 10

  condition {
    path_pattern {
      values = ["/submissions*", "/teams*", "/health"]
    }
  }
  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

output "dns_name" { value = aws_lb.main.dns_name }
output "target_api_arn" { value = aws_lb_target_group.api.arn }
output "target_lb_arn" { value = aws_lb_target_group.leaderboard.arn }