#!/usr/bin/env bash
set -e

BIN=./bin/worker
SECCOMP="$(pwd)/deployments/docker/seccomp/contestant.json"
PIDS=()

for i in 0 1 2 3 4; do
    PORT=$((9090 + i))
    $BIN --listen ":$PORT" --worker-id "worker-$i" --seccomp "$SECCOMP" &
    PIDS+=($!)
    echo "[launcher] worker-$i started on :$PORT (pid $!)"
done

echo "[launcher] all workers up. press Ctrl-C to stop."

cleanup() {
    echo "[launcher] stopping workers..."
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
}
trap cleanup INT TERM
wait