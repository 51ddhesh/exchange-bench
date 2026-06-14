terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

module "vpc" {
  source   = "./modules/vpc"
  project  = var.project
  vpc_cidr = var.vpc_cidr
}

module "ecr" {
  source  = "./modules/ecr"
  project = var.project
}

module "alb" {
  source            = "./modules/alb"
  project           = var.project
  vpc_id            = module.vpc.vpc_id
  public_subnet_ids = module.vpc.public_subnet_ids
}

module "redpanda" {
  source            = "./modules/redpanda"
  project           = var.project
  vpc_id            = module.vpc.vpc_id
  subnet_id         = module.vpc.private_subnet_ids[0]
  subnet_cidr       = module.vpc.private_subnet_cidrs[0]
  instance_type     = var.infra_instance_type
  security_group_id = module.vpc.internal_sg_id
  depends_on        = [module.vpc]
}

module "timescaledb" {
  source            = "./modules/timescaledb"
  project           = var.project
  vpc_id            = module.vpc.vpc_id
  subnet_id         = module.vpc.private_subnet_ids[0]
  subnet_cidr       = module.vpc.private_subnet_cidrs[0]
  instance_type     = var.infra_instance_type
  security_group_id = module.vpc.internal_sg_id
  db_password       = var.db_password
  schema_sql        = file("${path.module}/../internal/telemetry/schema.sql")
  depends_on        = [module.vpc]
}

module "ec2_workers" {
  source             = "./modules/ec2-workers"
  project            = var.project
  vpc_id             = module.vpc.vpc_id
  subnet_ids         = module.vpc.private_subnet_ids
  subnet_cidrs       = module.vpc.private_subnet_cidrs
  instance_type      = var.worker_instance_type
  worker_count       = var.worker_count
  security_group_id  = module.vpc.internal_sg_id
  ecr_worker_url     = module.ecr.worker_url
  ecr_api_url        = module.ecr.api_url
  ecr_compiler_url   = module.ecr.compiler_url
  ecr_runner_url     = module.ecr.runner_url
  ecr_contestant_url = module.ecr.contestant_url
  aws_region         = var.aws_region
  redpanda_ip        = module.redpanda.private_ip
  timescaledb_ip     = module.timescaledb.private_ip
  alb_target_api_arn = module.alb.target_api_arn
  db_password        = var.db_password
  s3_bucket_arn      = module.s3.bucket_arn
  sqs_queue_arn      = module.sqs.queue_arn
  s3_bucket_name     = module.s3.bucket_name
  sqs_queue_url      = module.sqs.queue_url
  depends_on         = [module.vpc]
}

module "ecs" {
  source              = "./modules/ecs"
  project             = var.project
  vpc_id              = module.vpc.vpc_id
  private_subnet_ids  = module.vpc.private_subnet_ids
  security_group_id   = module.vpc.internal_sg_id
  alb_target_api_arn  = module.alb.target_api_arn
  alb_target_lb_arn   = module.alb.target_lb_arn
  aws_region          = var.aws_region
  ecr_api_url         = module.ecr.api_url
  ecr_ingester_url    = module.ecr.ingester_url
  ecr_leaderboard_url = module.ecr.leaderboard_url
  redpanda_ip         = module.redpanda.private_ip
  timescaledb_ip      = module.timescaledb.private_ip
  worker_ips          = module.ec2_workers.private_ips
  db_password         = var.db_password
  cpu                 = var.ecs_cpu
  memory              = var.ecs_memory
}
module "s3" {
  source  = "./modules/s3"
  project = var.project
}

module "sqs" {
  source  = "./modules/sqs"
  project = var.project
}

module "lambda" {
  source        = "./modules/lambda"
  project       = var.project
  sqs_queue_arn = module.sqs.queue_arn
  s3_bucket_arn = module.s3.bucket_arn
  image_uri     = "${module.ecr.lambda_compiler_url}:latest"
}
