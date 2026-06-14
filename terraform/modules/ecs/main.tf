variable "project" {}
variable "vpc_id" {}
variable "private_subnet_ids" {}
variable "security_group_id" {}
variable "alb_target_api_arn" {}
variable "alb_target_lb_arn" {}
variable "aws_region" {}
variable "ecr_api_url" {}
variable "ecr_ingester_url" {}
variable "ecr_leaderboard_url" {}
variable "redpanda_ip" {}
variable "timescaledb_ip" {}
variable "worker_ips" {}
variable "db_password" { sensitive = true }
variable "cpu" {}
variable "memory" {}

resource "aws_iam_role" "ecs_task_exec" {
  name = "${var.project}-ecs-exec-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ecs_exec" {
  role       = aws_iam_role.ecs_task_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_cloudwatch_log_group" "ecs" {
  name              = "/ecs/${var.project}"
  retention_in_days = 7
}

resource "aws_ecs_cluster" "main" {
  name = "${var.project}-cluster"
}

locals {
  db_dsn = "postgres://postgres:${var.db_password}@${var.timescaledb_ip}:5432/postgres"
}

# ── Ingester (Fargate) ────────────────────────────────────────────────────────

resource "aws_ecs_task_definition" "ingester" {
  family                   = "${var.project}-ingester"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.ecs_task_exec.arn

  container_definitions = jsonencode([{
    name  = "ingester"
    image = "${var.ecr_ingester_url}:latest"
    command = [
      "--brokers=${var.redpanda_ip}:9092",
      "--topic=telemetry-events",
      "--dsn=${local.db_dsn}"
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.ecs.name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = "ingester"
      }
    }
  }])
}

resource "aws_ecs_service" "ingester" {
  name            = "${var.project}-ingester"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.ingester.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = false
  }

  depends_on = [aws_iam_role_policy_attachment.ecs_exec]
}

# ── Leaderboard (Fargate) ─────────────────────────────────────────────────────

resource "aws_ecs_task_definition" "leaderboard" {
  family                   = "${var.project}-leaderboard"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.ecs_task_exec.arn

  container_definitions = jsonencode([{
    name  = "leaderboard"
    image = "${var.ecr_leaderboard_url}:latest"
    command = [
      "--listen=:8080",
      "--dsn=${local.db_dsn}"
    ]
    portMappings = [{
      containerPort = 8080
      protocol      = "tcp"
    }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.ecs.name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = "leaderboard"
      }
    }
  }])
}

resource "aws_ecs_service" "leaderboard" {
  name            = "${var.project}-leaderboard"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.leaderboard.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = var.alb_target_lb_arn
    container_name   = "leaderboard"
    container_port   = 8080
  }

  depends_on = [aws_iam_role_policy_attachment.ecs_exec]
}