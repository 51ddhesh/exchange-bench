variable "project" {}
variable "vpc_id" {}
variable "subnet_id" {}
variable "instance_type" {}
variable "security_group_id" {}

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

resource "aws_iam_role" "redpanda" {
  name = "${var.project}-redpanda-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "redpanda_ssm" {
  role       = aws_iam_role.redpanda.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "redpanda" {
  name = "${var.project}-redpanda-profile"
  role = aws_iam_role.redpanda.name
}

resource "aws_instance" "redpanda" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  subnet_id              = var.subnet_id
  vpc_security_group_ids = [var.security_group_id]
  iam_instance_profile   = aws_iam_instance_profile.redpanda.name

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail

    for i in $(seq 1 30); do
      apt-get -o Acquire::ForceIPv4=true update -y && break
      echo "apt-get update attempt $i failed, retrying in 10s..."
      sleep 10
    done

    for i in $(seq 1 10); do
      apt-get -o Acquire::ForceIPv4=true install -y docker.io && break
      echo "apt-get install attempt $i failed, retrying in 10s..."
      sleep 10
    done

    command -v docker || { echo "FATAL: Docker not installed after retries"; exit 1; }
    systemctl enable --now docker

    # Fetch this instance's private IP from EC2 metadata so Redpanda
    # advertises the correct address to other VPC clients.
    PRIVATE_IP=$(curl -sf --max-time 5 http://169.254.169.254/latest/meta-data/local-ipv4)

    docker run -d \
      --name redpanda \
      --restart always \
      -p 9092:9092 \
      docker.redpanda.com/redpandadata/redpanda:v24.1.1 \
      redpanda start \
        --overprovisioned \
        --smp=1 \
        --memory=512M \
        --reserve-memory=0M \
        --node-id=0 \
        --check=false \
        --kafka-addr=PLAINTEXT://0.0.0.0:9092 \
        --advertise-kafka-addr=PLAINTEXT://$PRIVATE_IP:9092

    # Wait for Redpanda to be ready, then create the topic.
    for i in $(seq 1 24); do
      docker exec redpanda rpk topic create telemetry-events \
        --brokers localhost:9092 \
        --partitions 10 \
        --replicas 1 && break || sleep 10
    done
  EOF

  tags = { Name = "${var.project}-redpanda" }
}

output "private_ip" { value = aws_instance.redpanda.private_ip }