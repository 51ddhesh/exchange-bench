variable "project"            {}
variable "vpc_id"             {}
variable "subnet_ids"         {}
variable "instance_type"      {}
variable "worker_count"       {}
variable "security_group_id"  {}
variable "ecr_worker_url"     {}
variable "ecr_api_url"        {}
variable "ecr_compiler_url"   {}
variable "ecr_runner_url"     {}
variable "ecr_contestant_url" {}
variable "aws_region"         {}
variable "redpanda_ip"        {}
variable "timescaledb_ip"     {}
variable "db_password"        { sensitive = true }

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
  count                  = var.worker_count - 1
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  subnet_id              = var.subnet_ids[(count.index + 1) % length(var.subnet_ids)]
  vpc_security_group_ids = [var.security_group_id]
  iam_instance_profile   = aws_iam_instance_profile.worker.name

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail
    apt-get update -y
    apt-get install -y docker.io awscli
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

  tags = { Name = "${var.project}-worker-$((count.index + 1)}" }
}

# Worker-0 also runs the api. Created after workers 1-4 so their IPs are known.
locals {
  other_worker_ips  = aws_instance.workers[*].private_ip
  worker_grpc_addrs = join(",", concat(
    ["localhost:9090"],
    [for i, ip in local.other_worker_ips : "${ip}:${9091 + i}"]
  ))
}

resource "aws_instance" "worker0" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  subnet_id              = var.subnet_ids[0]
  vpc_security_group_ids = [var.security_group_id]
  iam_instance_profile   = aws_iam_instance_profile.worker.name

  depends_on = [aws_instance.workers]

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail
    apt-get update -y
    apt-get install -y docker.io awscli
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
      ${var.ecr_api_url}:latest \
      --listen=:8081 \
      --workers=${local.worker_grpc_addrs} \
      --image=exchange-bench-contestant \
      --seccomp=deployments/docker/seccomp/contestant.json \
      --ticks=10000 \
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
output "worker0_id"        { value = aws_instance.worker0.id }
output "worker_grpc_addrs" { value = local.worker_grpc_addrs }
output "asg_name"          { value = "static-${var.project}-workers" }
