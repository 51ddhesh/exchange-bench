variable "project" {}

resource "aws_sqs_queue" "compiler_jobs" {
  name                        = "${var.project}-compiler-jobs"
  visibility_timeout_seconds  = 300 # 5 minutes for compilation
  message_retention_seconds   = 86400 # 1 day
}

output "queue_url" {
  value = aws_sqs_queue.compiler_jobs.url
}

output "queue_arn" {
  value = aws_sqs_queue.compiler_jobs.arn
}
