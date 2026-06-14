variable "project" {}
variable "vpc_id" {}
variable "subnet_id" {}
variable "instance_type" {}
variable "security_group_id" {}
variable "subnet_cidr" {}
variable "db_password" { sensitive = true }
variable "schema_sql" { description = "Contents of schema.sql, passed from root module" }

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

resource "aws_iam_role" "timescaledb" {
  name = "${var.project}-timescaledb-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "timescaledb_ssm" {
  role       = aws_iam_role.timescaledb.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "timescaledb" {
  name = "${var.project}-timescaledb-profile"
  role = aws_iam_role.timescaledb.name
}

resource "aws_instance" "timescaledb" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_id
  private_ip                  = cidrhost(var.subnet_cidr, 60)
  vpc_security_group_ids      = [var.security_group_id]
  iam_instance_profile        = aws_iam_instance_profile.timescaledb.name
  user_data_replace_on_change = true

  root_block_device {
    volume_size = 20
    volume_type = "gp3"
  }

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

    docker run -d \
      --name timescaledb \
      --restart always \
      -p 5432:5432 \
      -v timescaledb-data:/home/postgres/pgdata/data \
      -e POSTGRES_USER=postgres \
      -e "POSTGRES_PASSWORD=${var.db_password}" \
      -e POSTGRES_DB=postgres \
      timescale/timescaledb-ha:pg16

    for i in $(seq 1 30); do
      docker exec timescaledb pg_isready -U postgres && break || sleep 5
    done

    docker exec timescaledb psql -U postgres -d postgres <<'SCHEMA'
${var.schema_sql}
SCHEMA
  EOF

  tags = { Name = "${var.project}-timescaledb" }
}

output "private_ip" { value = aws_instance.timescaledb.private_ip }