variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-south-1"
}

variable "project" {
  description = "Project name prefix for all resources"
  type        = string
  default     = "exchange-bench"
}

variable "vpc_cidr" {
  type    = string
  default = "10.0.0.0/16"
}

variable "worker_instance_type" {
  description = "EC2 instance type for worker nodes"
  type        = string
  default     = "t3.xlarge"
}

variable "infra_instance_type" {
  description = "EC2 instance type for Redpanda and TimescaleDB"
  type        = string
  default     = "t3.large"
}

variable "worker_count" {
  description = "Number of worker nodes"
  type        = number
  default     = 5
}

variable "db_password" {
  description = "TimescaleDB password"
  type        = string
  sensitive   = true
  default     = "changeme_in_tfvars"
}

variable "ecs_cpu" {
  description = "Fargate task CPU units (256 = 0.25 vCPU)"
  type        = number
  default     = 512
}

variable "ecs_memory" {
  description = "Fargate task memory in MiB"
  type        = number
  default     = 1024
}