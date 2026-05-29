# exchange-bench

Distributed exchange benchmarking suite for evaluating contestant trading algorithms against a deterministic, statistically-invariant market simulator.

## Packages

### orderbook
Reference matching engine. Price-time priority FIFO book with arena allocator (zero-alloc hot path for `Order` structs), node pool for linked-list nodes, and channel-based actor serialisation via `Engine.Run()`. A `Sequencer` layer maps external string order IDs to internal `uint64` IDs and stamps arrival times.

### workload
Deterministic market regime generator. `Generate(seed, totalTicks)` produces a `[]Tick` sequence using a log-normal mid-price walk and 5 Gaussian-blended regimes (Warmup, Normal, MarketMaking, CancelStorm, Spike). Statistically invariant; same seed always produces byte-identical output across platforms.

### protocol
Typed wire protocol for contestant communication. `WriteTick` serialises ADD/CAN ticks to ASCII lines (`ADD o1 B L 100.0000 10\n`). `ReadResponse` parses contestant ACK/FILL/REJ lines from stdout. All parse errors are non-fatal `ParseError` values.

### runner
Single-host dispatch loop. `Runner.Run()` sends ticks to a sandboxed contestant at a configurable rate, collects responses via `collectUntilACK()` (accumulating FILLs before the terminal ACK/REJ), and records latency in an HDR histogram. Production sandbox uses a hardened Docker container (no network, read-only rootfs, 64 MB tmpfs, 2 CPUs, 512 MB memory, no swap, all caps dropped, seccomp). Handles coordinated omission by measuring from the intended ticker fire time.

### validator
Tick-by-tick correctness scoring. Replays each `TickResult` through an independent reference `Engine` + `Sequencer` (no shared state with the runner) and compares contestant output against reference truth. Detects `OVERFILL`, `UNDERFILL`, `PRICE_TIME_PRIORITY`, `WRONG_EXEC_PRICE`, `ZOMBIE_FILL`, and `CANCEL_MISMATCH` violations.

### botworker
Open-loop tick firer and gRPC worker server. The `firer` spin-waits to a coordinated start time, launches a sandbox, then dispatches ticks open-loop via a ticker-driven writer goroutine while a reader goroutine collects ACK/REJ responses and records RTT latency. The `workerServer` implements the `WorkerService` gRPC interface with a state machine (`idle → prepared → firing → done`), streaming telemetry events back to the coordinator.

### coordinator
Distributed evaluation orchestration. A 3-phase execution model: **Prepare** shards the workload across workers via gRPC, **Fire** coordinates a simultaneous start across the fleet with a ramp-up loop (doubling rate per interval until saturation), and **Collect** gathers and merges per-worker metrics using weighted-average latency aggregation.

### coordinator/proto
Protobuf definitions and generated Go code for the `WorkerService` gRPC API: `Prepare`, `Fire` (server-streaming `TelemetryEvent`), `SetRate`, and `CollectMetrics`. The `Tick` message mirrors `workload.Tick` for cross-process serialisation.

## CLI Binaries

| Binary | Path | Description |
|--------|------|-------------|
| `agent` | `cmd/agent` | Single-host evaluation. Generates workload, starts sandbox, dispatches ticks, scores via validator, prints latency/correctness report. |
| `contestant` | `cmd/contestant` | Reference contestant. Reads ADD/CAN from stdin, writes FILL/ACK/REJ to stdout. Backed by the reference matching engine; any correct platform must score it at 100%. |
| `coordinator` | `cmd/coordinator` | Distributed evaluation orchestrator. Connects to workers via gRPC, shards workload, coordinates fire time, ramps rate, detects saturation, merges results. |
| `worker` | `cmd/worker` | Bot fleet worker. Hosts the `WorkerService` gRPC server, delegates to `botworker.WorkerServiceServer`. |

## Getting Started

### Prerequisites

- Go 1.26+
- Docker (with rootless setup recommended)
- Built contestant image: `docker build -t contestant -f Dockerfile.contestant .`

### Build

```bash
# Single-host binary
go build -o bin/agent ./cmd/agent

# Distributed evaluation binaries (required for launch scripts)
go build -o bin/worker ./cmd/worker
go build -o bin/coordinator ./cmd/coordinator
```

### Single-host Evaluation

```bash
go run ./cmd/agent --image contestant --ticks 500 --rate 100
```

### Distributed Evaluation

Build the binaries first, then use the launch scripts or run manually:

```bash
# Using launch scripts (builds must be in bin/)
./scripts/run_workers.sh
./scripts/run_coordinators.sh

# Or run manually:
go run ./cmd/worker --listen :9090 --worker-id node-1 &
go run ./cmd/worker --listen :9091 --worker-id node-2 &
go run ./cmd/coordinator --workers localhost:9090,localhost:9091 \
  --image contestant --ticks 100000 --max-rate 50000
```
