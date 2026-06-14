#!/usr/bin/env bash

sleep 300

set TSDB_ID $(aws ec2 describe-instances \
  --region ap-south-1 \
  --filters "Name=tag:Name,Values=exchange-bench-timescaledb" \
            "Name=instance-state-name,Values=running" \
  --query 'Reservations[0].Instances[0].InstanceId' \
  --output text)

set CMD_ID $(aws ssm send-command \
  --region ap-south-1 \
  --instance-ids $TSDB_ID \
  --document-name AWS-RunShellScript \
  --parameters commands='["docker ps -a", "tail -n 10 /var/log/cloud-init-output.log"]' \
  --query 'Command.CommandId' --output text)

sleep 60

aws ssm get-command-invocation \
  --region ap-south-1 \
  --instance-id $TSDB_ID \
  --command-id $CMD_ID \
  --query '{Status:Status,Stdout:StandardOutputContent}' \
  --output json

