terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

# This environment calls the root module with dev variable values.
# Run from this directory: terraform init && terraform apply
module "exchange_bench" {
  source = "../../"

  aws_region           = var.aws_region
  project              = var.project
  worker_instance_type = var.worker_instance_type
  infra_instance_type  = var.infra_instance_type
  worker_count         = var.worker_count
  db_password          = var.db_password
  ecs_cpu              = var.ecs_cpu
  ecs_memory           = var.ecs_memory
}

variable "aws_region" {}
variable "project" {}
variable "worker_instance_type" {}
variable "infra_instance_type" {}
variable "worker_count" {}
variable "db_password" { sensitive = true }
variable "ecs_cpu" {}
variable "ecs_memory" {}

output "alb_dns_name" { value = module.exchange_bench.alb_dns_name }
output "ecr_urls" {
  value = {
    api         = module.exchange_bench.ecr_api_url
    worker      = module.exchange_bench.ecr_worker_url
    ingester    = module.exchange_bench.ecr_ingester_url
    leaderboard = module.exchange_bench.ecr_leaderboard_url
    compiler    = module.exchange_bench.ecr_compiler_url
    runner      = module.exchange_bench.ecr_runner_url
    contestant  = module.exchange_bench.ecr_contestant_url
  }
}