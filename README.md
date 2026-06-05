# exchange-bench

Distributed exchange benchmarking suite for evaluating contestant trading algorithms against a deterministic, statistically-invariant market simulator.

> **⚠ Tests on `main` reference the old pipe-based interfaces and have not yet been updated for the new WebSocket protocol.** Switch to the `feat/compile` branch to run the tests against the stable stdin/stdout build.
>
> ```bash
> git checkout feat/compile
> ```

## Packages

### orderbook
Reference matching engine. Price-time priority FIFO book with arena allocator (zero-alloc hot path for `Order` structs), node pool for linked-list nodes, and channel-based actor serialisation via `Engine.Run()`. A `Sequencer` layer maps external string order IDs to internal `uint64` IDs and stamps arrival times.

### workload
Deterministic market regime generator. `Generate(seed, totalTicks)` produces a `[]Tick` sequence using a log-normal mid-price walk and 5 Gaussian-blended regimes (Warmup, Normal, MarketMaking, CancelStorm, Spike). Statistically invariant; same seed always produces byte-identical output across platforms.

### protocol
Typed wire protocol for contestant communication. `WriteTick` serialises ADD/CAN ticks to ASCII lines (`ADD o1 B L 100.0000 10\n`). `ReadResponse` parses contestant ACK/FILL/REJ lines from stdout. All parse errors are non-fatal `ParseError` values.

### runner
Single-bot closed-loop dispatch. `Runner.Run()` dials a contestant WebSocket endpoint, sends each tick, waits for ACK/REJ, and records latency in an HDR histogram. Used for the coordinator's smoke test phase. Handles coordinated omission by measuring from the intended ticker fire time.

### validator
Tick-by-tick correctness scoring. Replays each `TickResult` through an independent reference `Engine` + `Sequencer` (no shared state with the runner) and compares contestant output against reference truth. Detects `OVERFILL`, `UNDERFILL`, `PRICE_TIME_PRIORITY`, `WRONG_EXEC_PRICE`, `ZOMBIE_FILL`, and `CANCEL_MISMATCH` violations.

### botworker
Open-loop tick firer and gRPC worker server. The `firer` spin-waits to a coordinated start time, then launches a fleet of **bots** (configurable count), each dialing the contestant WebSocket endpoint independently. Each bot runs a ticker-driven `writeLoop` and a `readLoop` that accumulates FILLs per order, records RTT latency in a per-bot HDR histogram, and emits one `TelemetryEvent` per terminal ACK/REJ. A per-bot **validator correlator** feeds results through `validator.Consume()` to detect violations at line speed. The `workerServer` implements the `WorkerService` gRPC interface with a state machine (`idle → prepared → firing → done`), streaming telemetry events back to the coordinator.

### coordinator
Distributed evaluation orchestration. A 3-phase execution model: **Prepare** shards the workload across workers via gRPC, **Fire** coordinates a simultaneous start across the fleet with a ramp-up loop (doubling rate per interval until saturation), and **Collect** gathers and merges per-worker metrics using weighted-average latency aggregation. Before the distributed phase, a **smoke test** runs the first 10K ticks through a single closed-loop runner + validator, enforcing an 80% correctness gate.

### coordinator/proto
Protobuf definitions and generated Go code for the `WorkerService` gRPC API: `Prepare`, `Fire` (server-streaming `TelemetryEvent`), `SetRate`, and `CollectMetrics`. The `Tick` message mirrors `workload.Tick` for cross-process serialisation.

## CLI Binaries

| Binary | Path | Description |
|--------|------|-------------|
| `agent` | `cmd/agent` | Single-host evaluation (pipe-based — checkout `feat/compile` first). |
| `contestant` | `cmd/contestant` | Reference contestant. WebSocket server on `:8080/orders`. Each connection gets an independent matching engine. Backed by the reference orderbook; any correct platform must score it at 100%. |
| `coordinator` | `cmd/coordinator` | Distributed evaluation orchestrator. Connects to workers via gRPC, runs smoke test against a pre-started contestant, then shards workload, coordinates fire time, ramps rate, detects saturation, merges results. |
| `worker` | `cmd/worker` | Bot fleet worker. Hosts the `WorkerService` gRPC server, delegates to `botworker.firer` which spawns parallel WebSocket bots. |

## Getting Started

### Prerequisites

- Go 1.26+

### Tests

Build and all tests pass on the `feat/compile` branch (the last stable state before the WebSocket transport migration). While tests on `main` are being updated:

```bash
git checkout feat/compile
go test ./... -v -count=1
git checkout main
```

Key test packages:

| Package | Tests | Status |
|---------|-------|--------|
| `internal/protocol` | Parse round-trips, error handling, edge cases | ✅ All pass |
| `internal/runner` | Pipeline dispatch, context cancel, parse errors | ✅ All pass |

### Build

```bash
# All binaries
go build -o bin/contestant ./cmd/contestant
go build -o bin/worker ./cmd/worker
go build -o bin/coordinator ./cmd/coordinator

# Single-host agent (pipe-based — checkout feat/compile first)
go build -o bin/agent ./cmd/agent
```

### Distributed Evaluation

The contestant runs as a standalone WebSocket server. Workers connect to it via a shared endpoint, each spawning multiple bots (default 200).

```bash
# 1. Build binaries (see above)
# 2. Start the contestant WebSocket server
./bin/contestant &

# 3. Start worker gRPC servers
bash scripts/run_workers.sh &
sleep 2

# 4. Run the coordinator
./bin/coordinator \
  --workers "localhost:9090,localhost:9091,localhost:9092,localhost:9093,localhost:9094" \
  --contestant-endpoint "ws://localhost:8080/orders" \
  --ticks 10000 \
  --init-rate 200 \
  --max-rate 5000 \
  --timeout 60s
```

The coordinator first runs a **smoke test** (10K ticks, closed-loop, 80% correctness gate required), then shards the workload across workers for the distributed open-loop phase.
