variable "project" {}
variable "vpc_id" {}
variable "subnet_ids" {}
variable "instance_type" {}
variable "worker_count" {}
variable "security_group_id" {}
variable "ecr_worker_url" {}
variable "ecr_api_url" {}
variable "ecr_compiler_url" {}
variable "ecr_runner_url" {}
variable "ecr_contestant_url" {}
variable "aws_region" {}
variable "redpanda_ip" {}
variable "timescaledb_ip" {}
variable "subnet_cidrs" {}
variable "alb_target_api_arn" {}
variable "db_password" { sensitive = true }

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

data "aws_caller_identity" "current" {}

resource "aws_lb_target_group_attachment" "api" {
  target_group_arn = var.alb_target_api_arn
  target_id        = aws_instance.worker0.id
  port             = 8081
}

resource "aws_iam_role" "worker" {
  name = "${var.project}-worker-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "worker_ssm" {
  role       = aws_iam_role.worker.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "worker_ecr" {
  name = "${var.project}-worker-ecr"
  role = aws_iam_role.worker.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ecr:GetAuthorizationToken",
        "ecr:BatchGetImage",
        "ecr:GetDownloadUrlForLayer"
      ]
      Resource = "*"
    }]
  })
}

resource "aws_iam_instance_profile" "worker" {
  name = "${var.project}-worker-profile"
  role = aws_iam_role.worker.name
}

# Workers 1-4: plain workers, no api
resource "aws_instance" "workers" {
  count                       = var.worker_count - 1
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_ids[(count.index + 1) % length(var.subnet_ids)]
  private_ip                  = cidrhost(var.subnet_cidrs[(count.index + 1) % length(var.subnet_cidrs)], 100 + count.index + 1)
  vpc_security_group_ids      = [var.security_group_id]
  iam_instance_profile        = aws_iam_instance_profile.worker.name
  user_data_replace_on_change = true

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail

    for i in $(seq 1 30); do
      apt-get -o Acquire::ForceIPv4=true update -y && break
      echo "apt-get update attempt $i failed, retrying in 10s..."
      sleep 10
    done

    for i in $(seq 1 10); do
      apt-get -o Acquire::ForceIPv4=true install -y docker.io awscli && break
      echo "apt-get install attempt $i failed, retrying in 10s..."
      sleep 10
    done

    command -v docker || { echo "FATAL: Docker not installed after retries"; exit 1; }
    systemctl enable --now docker

    aws ecr get-login-password --region ${var.aws_region} \
      | docker login --username AWS --password-stdin \
        ${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com

    docker pull ${var.ecr_worker_url}:latest
    docker pull ${var.ecr_compiler_url}:latest
    docker pull ${var.ecr_runner_url}:latest
    docker pull ${var.ecr_contestant_url}:latest

    docker tag ${var.ecr_compiler_url}:latest   exchange-bench-compiler
    docker tag ${var.ecr_runner_url}:latest     exchange-bench-runner
    docker tag ${var.ecr_contestant_url}:latest exchange-bench-contestant

    mkdir -p /tmp/exchange-bench

    GRPC_PORT=$((9091 + ${count.index}))
    docker run -d \
      --name worker-$((${count.index} + 1)) \
      --restart always \
      --network host \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v /tmp/exchange-bench:/tmp/exchange-bench \
      ${var.ecr_worker_url}:latest \
      --listen=:$GRPC_PORT \
      --worker-id=worker-$((${count.index} + 1)) \
      --seccomp=deployments/docker/seccomp/contestant.json
  EOF

  tags = { Name = "${var.project}-worker-${count.index + 1}" }
}

locals {
  other_worker_ips  = aws_instance.workers[*].private_ip
  worker_grpc_addrs = join(",", concat(
    ["localhost:9090"],
    [for ip in local.other_worker_ips : "${ip}:9090"]
  ))
}

resource "aws_instance" "worker0" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_ids[0]
  private_ip                  = cidrhost(var.subnet_cidrs[0], 100)
  vpc_security_group_ids      = [var.security_group_id]
  iam_instance_profile        = aws_iam_instance_profile.worker.name
  user_data_replace_on_change = true

  depends_on = [aws_instance.workers]

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail

    for i in $(seq 1 30); do
      apt-get -o Acquire::ForceIPv4=true update -y && break
      echo "apt-get update attempt $i failed, retrying in 10s..."
      sleep 10
    done

    for i in $(seq 1 10); do
      apt-get -o Acquire::ForceIPv4=true install -y docker.io awscli && break
      echo "apt-get install attempt $i failed, retrying in 10s..."
      sleep 10
    done

    command -v docker || { echo "FATAL: Docker not installed after retries"; exit 1; }
    systemctl enable --now docker

    aws ecr get-login-password --region ${var.aws_region} \
      | docker login --username AWS --password-stdin \
        ${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com

    docker pull ${var.ecr_worker_url}:latest
    docker pull ${var.ecr_compiler_url}:latest
    docker pull ${var.ecr_runner_url}:latest
    docker pull ${var.ecr_contestant_url}:latest
    docker pull ${var.ecr_api_url}:latest

    docker tag ${var.ecr_compiler_url}:latest   exchange-bench-compiler
    docker tag ${var.ecr_runner_url}:latest     exchange-bench-runner
    docker tag ${var.ecr_contestant_url}:latest exchange-bench-contestant

    mkdir -p /tmp/exchange-bench

    docker run -d \
      --name worker-0 \
      --restart always \
      --network host \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v /tmp/exchange-bench:/tmp/exchange-bench \
      ${var.ecr_worker_url}:latest \
      --listen=:9090 \
      --worker-id=worker-0 \
      --seccomp=deployments/docker/seccomp/contestant.json

    docker run -d \
      --name api \
      --restart always \
      --network host \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v /tmp/exchange-bench:/tmp/exchange-bench \
      -e S3_BUCKET=${var.s3_bucket_name} \
      -e SQS_QUEUE_URL=${var.sqs_queue_url} \
      -e AWS_DEFAULT_REGION=${var.aws_region} \
      ${var.ecr_api_url}:latest \
      --listen=:8081 \
      --workers=${local.worker_grpc_addrs} \
      --image=exchange-bench-contestant \
      --seccomp=deployments/docker/seccomp/contestant.json \
      --ticks=1000000 \
      --init-rate=200 \
      --max-rate=5000 \
      --redpanda-brokers=${var.redpanda_ip}:9092 \
      --redpanda-topic=telemetry-events \
      --dsn=postgres://postgres:${var.db_password}@${var.timescaledb_ip}:5432/postgres
  EOF

  tags = { Name = "${var.project}-worker-0" }
}

output "private_ips" {
  value = concat(
    [aws_instance.worker0.private_ip],
    aws_instance.workers[*].private_ip
  )
}
output "worker0_id" { value = aws_instance.worker0.id }
output "worker_grpc_addrs" { value = local.worker_grpc_addrs }
output "asg_name" { value = "static-${var.project}-workers" }

resource "aws_iam_role_policy" "worker_async" {
  name = "${var.project}-worker-async"
  role = aws_iam_role.worker.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = [
          "s3:GetObject",
          "s3:PutObject"
        ]
        Resource = "${var.s3_bucket_arn}/*"
      },
      {
        Effect   = "Allow"
        Action   = [
          "sqs:SendMessage",
          "sqs:ReceiveMessage",
          "sqs:DeleteMessage",
          "sqs:GetQueueAttributes"
        ]
        Resource = var.sqs_queue_arn
      }
    ]
  })
}