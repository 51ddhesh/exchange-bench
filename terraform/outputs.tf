output "alb_dns_name" {
  description = "ALB DNS name — use this to reach the API and leaderboard"
  value       = module.alb.dns_name
}

output "ecr_api_url" {
  value = module.ecr.api_url
}

output "ecr_worker_url" {
  value = module.ecr.worker_url
}

output "ecr_ingester_url" {
  value = module.ecr.ingester_url
}

output "ecr_leaderboard_url" {
  value = module.ecr.leaderboard_url
}

output "ecr_compiler_url" {
  value = module.ecr.compiler_url
}

output "ecr_runner_url" {
  value = module.ecr.runner_url
}

output "ecr_contestant_url" {
  value = module.ecr.contestant_url
}

output "worker_asg_name" {
  value = module.ec2_workers.asg_name
}