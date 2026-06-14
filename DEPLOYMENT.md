# Exchange Bench Deployment Guide

This document outlines the steps to successfully deploy the Exchange Bench infrastructure on AWS. 

## 1. Prerequisites

- **Terraform** >= 1.4.x
- **AWS CLI** configured with administrator credentials
- **Docker** for building service images

## 2. Infrastructure Setup

The infrastructure is fully defined in Terraform and utilizes an Application Load Balancer (ALB) routing traffic to an API node (`worker-0`), backed by an EC2 auto-scaling fleet of worker nodes, TimescaleDB, and Redpanda.

1. Navigate to the `terraform` directory:
   ```bash
   cd terraform
   ```
2. Initialize Terraform:
   ```bash
   terraform init
   ```
3. Deploy the infrastructure using the dev environment variables:
   ```bash
   terraform apply -var-file="environments/dev/terraform.tfvars"
   ```

## 3. Important Configuration Notes (IP Routing & Bug Fixes)

We encountered a few architectural issues during deployment which have been resolved on this branch:

1. **Route 53 IAM IP Drift**: Originally, the Terraform code relied on private Route 53 zones to provide internal DNS for EC2 instances. This led to frequent IAM `AccessDenied` errors during creation and deletion loops. To stabilize the cluster, Route 53 has been entirely removed. Instead, the cluster now explicitly uses **static internal IPs** (assigned via `cidrhost` offsets) for Redpanda (`.50`), TimescaleDB (`.60`), and the EC2 worker fleet (`.100+`).
2. **Worker gRPC Ports**: A bug in the initial `ec2-workers` setup configured the API server to connect to worker instances on incrementing ports (`9091`, `9092`, etc.). However, each worker instance natively listens on port `9090`. The module has been updated to consistently use port `9090` for all gRPC worker traffic.

## 4. Docker Images

If you need to push new images to ECR, authenticate your Docker client using:
```bash
aws ecr get-login-password --region ap-south-1 | docker login --username AWS --password-stdin <your-account-id>.dkr.ecr.ap-south-1.amazonaws.com
```

The infrastructure expects the following ECR repositories to be populated with a `latest` tag:
- `exchange-bench-api`
- `exchange-bench-worker`
- `exchange-bench-compiler`
- `exchange-bench-runner`
- `exchange-bench-contestant`
- `exchange-bench-leaderboard`
- `exchange-bench-ingester`

## 5. Testing the Deployment

Once `terraform apply` finishes and the instances boot, you can submit test bot programs using `curl` against the ALB's DNS name:

```bash
ALB_URL=$(terraform output -raw alb_dns_name)

# Submit a test bot
curl -X POST -F "team_id=alpha" -F "language=go" -F "source=@tests_submissions/team-alpha.go" http://$ALB_URL/submissions

# Check the bot's status
curl -s http://$ALB_URL/submissions/alpha_1
```
