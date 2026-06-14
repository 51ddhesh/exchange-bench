#!/bin/bash
go build -o api ./cmd/api
go build -o worker ./cmd/worker

./worker --listen=:9090 --worker-id=worker-0 --seccomp=deployments/docker/seccomp/contestant.json --local=true > worker-0.log 2>&1 &
./worker --listen=:9091 --worker-id=worker-1 --seccomp=deployments/docker/seccomp/contestant.json --local=true > worker-1.log 2>&1 &
./worker --listen=:9092 --worker-id=worker-2 --seccomp=deployments/docker/seccomp/contestant.json --local=true > worker-2.log 2>&1 &
./worker --listen=:9093 --worker-id=worker-3 --seccomp=deployments/docker/seccomp/contestant.json --local=true > worker-3.log 2>&1 &
./worker --listen=:9094 --worker-id=worker-4 --seccomp=deployments/docker/seccomp/contestant.json --local=true > worker-4.log 2>&1 &

sleep 2

./api --listen=:8081 --workers=localhost:9090,localhost:9091,localhost:9092,localhost:9093,localhost:9094 \
      --image=exchange-bench-contestant \
      --seccomp=deployments/docker/seccomp/contestant.json \
      --ticks=10000 \
      --init-rate=200 \
      --max-rate=5000 \
      --redpanda-brokers=localhost:19092 \
      --redpanda-topic=telemetry-events \
      --dsn=postgres://postgres:password@localhost:5432/postgres \
      --local=true > api.log 2>&1 &
