# ExchangeBench

**ExchangeBench** is a distributed, highly concurrent benchmarking platform designed to evaluate the performance and correctness of high-frequency trading matching engines. It simulates a realistic, distributed trading environment where thousands of concurrent bots connect via WebSockets to bombard a matching engine with limit order book workloads.

This repository contains the full source code for the platform, including the orchestrator, bot fleet workers, security sandbox, telemetry pipeline, and leaderboard.

## Project Structure

- **`cmd/`**: Entrypoints for the various services.
  - `api/`: The main API server that handles submissions and orchestrates runs.
  - `worker/`: The gRPC bot fleet worker that generates load.
  - `leaderboard/`: The real-time ranking server.
  - `contestant/`: A reference matching engine (Go) used for testing.
- **`internal/`**: Core logic (orderbook engine, workload generation, validation, runner).
- **`deployments/`**: Dockerfiles and seccomp profiles for the compilation and sandbox environments.

## Prerequisites

To run ExchangeBench locally, you will need:
- **Go 1.22+**
- **Docker & Docker Compose** (for running the sandboxes and infrastructure)
- **Node.js & npm** (if you wish to modify the leaderboard frontend)

*Note: The platform relies heavily on Linux-specific container features (network namespaces, seccomp). It is highly recommended to run this on a Linux host or a Linux VM.*

## Getting Started

### 1. Clone the Repository

```bash
git clone https://github.com/51ddhesh/exchange-bench.git
cd exchange-bench
```

### 2. Build the Docker Images

ExchangeBench uses two highly restricted Docker images to securely compile and run contestant code. You must build these before starting the platform:

```bash
# Build the compiler image (contains toolchains for C++, Rust, Go, Python)
docker build -t exchange-bench-compiler -f deployments/docker/Dockerfile.compiler .

# Build the runner image (the strict sandbox environment)
docker build -t exchange-bench-runner -f deployments/docker/Dockerfile.runner .
```

### 3. Start the Infrastructure

For a full local deployment, use `docker-compose` to spin up TimescaleDB (for telemetry), Redpanda (for high-throughput event streaming), the API server, the leaderboard, and the bot fleet workers.

```bash
docker-compose up -d
```

*This will start TimescaleDB on `:5432`, Redpanda on `:19092`, the API Server on `:8081`, and the Leaderboard on `:8080`.*

### 4. Submit a Test Bot

You can test the platform by submitting the reference Go matching engine provided in `cmd/contestant/main.go`.

```bash
curl -X POST \
  -F "team_id=test_team" \
  -F "language=go" \
  -F "source=@cmd/contestant/main.go" \
  http://localhost:8081/submissions
```

The API will return a JSON response containing a `submission_id`. You can track its progress by querying the API:

```bash
curl http://localhost:8081/submissions/<submission_id>
```

Once the run completes, check `http://localhost:8080` in your browser to see the real-time leaderboard!

## Architecture & Documentation

To understand the internal workings of ExchangeBench, please refer to the detailed documentation files provided:
- [**`DOC.md`**](DOC.md): Deep dive into the distributed architecture, scaling model, security boundaries, and validation algorithms.
- [**`GUIDE.md`**](GUIDE.md): The official manual for competitors, detailing WebSocket requirements, interaction protocols, and rule enforcement.

## Contributing

While this project was initially built for the IICPC Summer Hackathon, contributions are welcome. If you are modifying the platform, note that the core testing mechanism relies on exact determinism. Any changes to `internal/workload` or `internal/orderbook` must ensure that the generated order sequences remain perfectly reproducible across platforms.
