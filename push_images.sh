#!/usr/bin/env bash
set -euo pipefail

ACCOUNT_ID="${1:?usage: push_images.sh <account-id> <region>}"
REGION="${2:?usage: push_images.sh <account-id> <region>}"
ECR_BASE="${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com"
PROJECT="exchange-bench"

echo "==> Logging in to ECR"
aws ecr get-login-password --region "${REGION}" \
  | docker login --username AWS --password-stdin "${ECR_BASE}"

build_and_push() {
  local name="$1"
  local dockerfile="$2"
  local repo="${ECR_BASE}/${PROJECT}-${name}"
  echo "==> Building ${name}"
  docker build -t "${PROJECT}-${name}" -f "${dockerfile}" .
  docker tag "${PROJECT}-${name}:latest" "${repo}:latest"
  echo "==> Pushing ${name}"
  docker push "${repo}:latest"
}

build_and_push "api"          "Dockerfile.api"
build_and_push "worker"       "Dockerfile.worker"
build_and_push "ingester"     "Dockerfile.ingester"
build_and_push "leaderboard"  "Dockerfile.leaderboard"
build_and_push "compiler"     "Dockerfile.compiler"
build_and_push "runner"       "Dockerfile.runner"
build_and_push "contestant"   "Dockerfile.contestant"

echo ""
echo "All images pushed to ECR."
echo "ALB DNS: $(cd terraform && terraform output -raw alb_dns_name 2>/dev/null || echo 'run terraform apply first')"