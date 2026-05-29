#!/usr/bin/env bash
set -e

IMAGE=${1:-exchange-bench-contestant}

echo "[coordinator] waiting 2s for workers to settle..."
sleep 2

./bin/coordinator \
    --workers "localhost:9090,localhost:9091,localhost:9092,localhost:9093,localhost:9094" \
    --image  "$IMAGE" \
    --seed   42 \
    --ticks  1000000 \
    --init-rate 2000 \
    --max-rate  50000 \
    --ramp  3s \
    --timeout 300s