#!/usr/bin/env bash

for port in 9090 9091 9092 9093 9094; do
  echo -n "worker :$port — "
  grpc_health_probe -addr=localhost:$port 2>/dev/null || \
    curl -s --connect-timeout 1 localhost:$port > /dev/null 2>&1 && echo "up" || echo "down"
done
