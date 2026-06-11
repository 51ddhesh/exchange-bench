variable "project" {}

locals {
  repos = ["api", "worker", "ingester", "leaderboard", "compiler", "runner", "contestant"]
}

resource "aws_ecr_repository" "repos" {
  for_each             = toset(local.repos)
  name                 = "${var.project}-${each.key}"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = false
  }

  tags = { Name = "${var.project}-${each.key}" }
}

# Lifecycle policy: keep only last 5 images per repo to save storage.
resource "aws_ecr_lifecycle_policy" "keep_five" {
  for_each   = aws_ecr_repository.repos
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep last 5 images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 5
      }
      action = { type = "expire" }
    }]
  })
}

output "api_url"         { value = aws_ecr_repository.repos["api"].repository_url }
output "worker_url"      { value = aws_ecr_repository.repos["worker"].repository_url }
output "ingester_url"    { value = aws_ecr_repository.repos["ingester"].repository_url }
output "leaderboard_url" { value = aws_ecr_repository.repos["leaderboard"].repository_url }
output "compiler_url"    { value = aws_ecr_repository.repos["compiler"].repository_url }
output "runner_url"      { value = aws_ecr_repository.repos["runner"].repository_url }
output "contestant_url"  { value = aws_ecr_repository.repos["contestant"].repository_url }