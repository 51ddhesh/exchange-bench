# ExchangeBench — System Architecture & Diagrams

A visual architecture reference for ExchangeBench, the distributed exchange benchmarking platform.

---

## 1. System Overview

```mermaid
graph TD
    User(["Contestant Team"]) -->|"POST /submissions<br/>(multipart source file)"| API["API Server :8081<br/>(EC2 worker-0)"]

    API -->|"Upload source"| S3[("S3 Bucket<br/>artifacts")]
    API -->|"Enqueue job"| SQS[("SQS Queue<br/>compiler-jobs")]
    SQS -->|"Trigger"| Lambda["Lambda Compiler<br/>(3 GB RAM, toolchains baked in)"]
    Lambda -->|"Download source"| S3
    Lambda -->|"Upload binary"| S3
    Lambda -->|"POST /webhook/compiler"| API

    API -->|"gRPC StartSandbox<br/>(S3 binary URL)"| W0["Worker-0 :9090<br/>(same EC2 as API)"]
    W0 -->|"Download binary"| S3
    W0 -->|"docker run sandbox"| Sandbox["Contestant Container :8080<br/>(exchange-bench-internal)"]

    API -->|"library call"| Coord["Coordinator<br/>(same process as API)"]
    Coord -->|"gRPC Prepare/Fire"| W0
    Coord -->|"gRPC Prepare/Fire"| W1["Worker-1 :9090<br/>(separate EC2)"]
    Coord -->|"gRPC Prepare/Fire"| Wn["Worker-N :9090<br/>(separate EC2)"]

    W0 <-->|"WebSocket ADD/CAN<br/>ACK/FILL/REJ"| Sandbox
    W1 <-->|"WebSocket ADD/CAN<br/>ACK/FILL/REJ"| Sandbox
    Wn <-->|"WebSocket ADD/CAN<br/>ACK/FILL/REJ"| Sandbox

    W0 -.->|"gRPC TelemetryEvent stream"| Coord
    W1 -.->|"gRPC TelemetryEvent stream"| Coord
    Wn -.->|"gRPC TelemetryEvent stream"| Coord

    Coord -->|"producerCh"| Producer["Telemetry Producer"]
    Producer -->|"franz-go publish"| RP[("Redpanda<br/>telemetry-events")]

    RP -->|"kafka consume"| Ingester["Ingester<br/>(ECS Fargate)"]
    Ingester -->|"CopyFrom batch"| TSDB[("TimescaleDB<br/>(EC2 Docker)")]

    LB["Leaderboard :8080<br/>(ECS Fargate)"] -->|"poll every 1s"| TSDB
    LB -->|"WebSocket broadcast"| Frontend(["Browser"])

    style Sandbox fill:#ff6b6b,stroke:#c92a2a,color:#fff
    style Lambda fill:#f59f00,stroke:#e67700,color:#fff
    style S3 fill:#1971c2,stroke:#1864ab,color:#fff
    style SQS fill:#1971c2,stroke:#1864ab,color:#fff
    style RP fill:#7950f2,stroke:#5f3dc4,color:#fff
    style TSDB fill:#20c997,stroke:#0b7285,color:#fff
```

---

## 2. Submission Lifecycle

```mermaid
sequenceDiagram
    participant User
    participant API as API Server<br/>(EC2 worker-0)
    participant S3 as S3 Bucket
    participant SQS as SQS Queue
    participant Lambda as Lambda Compiler<br/>(3 GB RAM)
    participant Worker as Worker Node<br/>(EC2)
    participant Sandbox as Contestant Container
    participant Coord as Coordinator<br/>(in-process with API)
    participant Fleet as Bot Fleet (N Workers)
    participant RP as Redpanda
    participant Ingester
    participant TSDB as TimescaleDB
    participant LB as Leaderboard

    User->>API: POST /submissions (source + lang + team_id)
    API->>API: Validate extension, 1MB limit
    API->>API: Assign submission_id (team_1)
    API->>S3: Upload source file
    API->>SQS: Enqueue job {submission_id, s3_key, language}
    API-->>User: HTTP 202 Accepted {submission_id, status: queued}

    SQS->>Lambda: Trigger (event with job metadata)
    Lambda->>S3: Download source
    Lambda->>Lambda: Compile (go/rustc/g++/python)<br/>50MB binary cap
    Lambda->>S3: Upload compiled binary
    Lambda->>API: POST /webhook/compiler {submission_id, artifact_s3_key}

    API->>Worker: gRPC StartSandbox (s3_binary_url)
    Worker->>S3: Download binary
    Worker->>Sandbox: docker run --network=exchange-bench-internal<br/>--read-only --cap-drop=ALL --memory=512m
    Sandbox-->>Worker: Readiness probe OK (WebSocket :8080)
    Worker-->>API: Sandbox endpoint

    API->>Coord: Run(ticks, endpoint)

    Note over Coord,Fleet: Phase 1 — Smoke Test (10K ticks, closed-loop)
    Coord->>Sandbox: Single-bot WebSocket
    Sandbox-->>Coord: ACK/FILL/REJ
    Coord->>Coord: Validate against reference engine
    Coord->>Coord: Enforce 80% correctness gate

    Note over Coord,Fleet: Phase 2 — Distributed Load Test
    Coord->>Fleet: gRPC Prepare (shard ticks + rate)
    Coord->>Fleet: gRPC Fire (fire_at_unix_ns + endpoint)

    loop Rate Ramp-Up (double every interval until saturation)
        Fleet->>Sandbox: Open-loop WebSocket ticks (200 bots/worker)
        Sandbox-->>Fleet: ACK/FILL/REJ responses
        Fleet-.>>Coord: gRPC TelemetryEvent stream
        Coord->>Fleet: gRPC SetRate (2× rate)
        Coord->>Coord: Detect saturation (2 windows ackRate < 95% sendRate)
    end

    Coord->>Fleet: gRPC CollectMetrics
    Fleet-->>Coord: HDR histograms + counts
    Coord->>Coord: Merge metrics, compute PeakTPS + CapacityTPS
    Coord->>RP: Publish sentinel (__RUN_COMPLETE__) via producerCh

    RP->>Ingester: Consume batch
    Ingester->>TSDB: CopyFrom telemetry_events
    Ingester->>TSDB: approx_percentile → upsert run_scores (p50/p90/p99)

    API->>TSDB: Upsert run_scores (PeakTPS, CapacityTPS, correctness, composite_score)
    API->>Sandbox: Kill container

    LB->>TSDB: Poll run_scores every 1s
    LB->>LB: buildTiers → intra-tier normalization
    LB-->>User: WebSocket broadcast (ranked leaderboard)
```

---

## 3. Telemetry Pipeline

```mermaid
graph LR
    subgraph "Per-Bot (200 per worker)"
        B1["Bot 1"] --> WS1["writeLoop<br/>(open-loop ticker)"]
        B1 --> RS1["readLoop<br/>(FILL accumulator)"]
        RS1 -->|"TelemetryEvent"| EVT1["event channel"]
    end

    subgraph "gRPC Streams"
        EVT1 -->|"stream.Send()"| GRPC["Worker gRPC Server"]
        GRPC -->|"stream.Recv()"| FAN["Coordinator Fan-In<br/>(telemetryCh)"]
    end

    subgraph "Coordinator Process"
        FAN -->|"stamp run_id + submission_id"| PC["producerCh (cap 8192)<br/>non-blocking send"]
        FAN -->|"streams EOF"| SENT["Publish __RUN_COMPLETE__<br/>sentinel"]
        PC -->|"if channel full"| DROP["⚠ Dropped<br/>(event lost)"]
    end

    subgraph "Async Pipeline"
        PC --> PROD["Producer.Run()"]
        SENT --> PROD
        PROD -->|"franz-go batch"| RP[("Redpanda<br/>partition key = submission_id")]
        RP --> ING["Ingester"]
        ING -->|"1000-row batches<br/>+ 500ms flush timer"| TSDB[("TimescaleDB<br/>telemetry_events hypertable")]
    end

    subgraph "Sentinel Flow"
        ING -->|"Detect sentinel"| FINAL["finalizeRun()"]
        FINAL -->|"approx_percentile(0.50/0.90/0.99)"| TSDB
        FINAL -->|"UPSERT p50, p90, p99"| SCORES[("run_scores table")]
    end

    style DROP fill:#ff6b6b,stroke:#c92a2a,color:#fff
    style RP fill:#7950f2,stroke:#5f3dc4,color:#fff
    style TSDB fill:#20c997,stroke:#0b7285,color:#fff
    style SCORES fill:#20c997,stroke:#0b7285,color:#fff
```

---

## 4. Dual Scoring System

ExchangeBench uses **two distinct scoring formulas** at different stages:

```mermaid
graph TD
    subgraph "Stage 1: Best-Submission Selection (API Server)"
        M1["PeakTPS"] --> MUL["Multiplicative Formula"]
        M2["Correctness"] --> MUL
        M3["avg(P50, P90, P99)"] --> LAT["1000 / GREATEST(1, avg_latency)"]
        LAT --> MUL
        MUL -->|"composite_score column"| DB[("run_scores")]
        DB -->|"DISTINCT ON team_id<br/>ORDER BY composite_score DESC"| BEST["Best submission per team"]
    end

    subgraph "Stage 2: Leaderboard Ranking (Leaderboard Server)"
        BEST --> TIER{"Group by tier"}
        TIER -->|"cpp/rust"| SYS["Systems"]
        TIER -->|"go"| GC["GC"]
        TIER -->|"python"| INT["Interpreted"]

        SYS --> NORM["Intra-tier normalization<br/>normCapTPS = CapTPS / max_CapTPS_in_tier<br/>normLatency = 1 - (P99 / max_P99_in_tier)"]
        GC --> NORM
        INT --> NORM

        NORM --> ADD["Additive Formula<br/>correctness×0.4 + normCapTPS×0.4 + normLatency×0.2"]
        ADD --> RANK["Final Ranking"]
    end

    style MUL fill:#4c6ef5,stroke:#364fc7,color:#fff
    style ADD fill:#f76707,stroke:#d9480f,color:#fff
    style DB fill:#20c997,stroke:#0b7285,color:#fff
```

| Stage | Formula | Used For |
|---|---|---|
| **Selection** | `PeakTPS × correctness × (1000 / avg(P50,P90,P99))` | Picking each team's best submission |
| **Ranking** | `correctness×0.4 + (capTPS/maxCapTPS)×0.4 + (1-P99/maxP99)×0.2` | Final leaderboard display with per-tier normalization |

**Key distinctions:**
- **PeakTPS** = highest raw ACK rate in any 1-second window
- **CapacityTPS** = highest ACK rate in a window where correctness ≥ 95%
- Selection formula uses PeakTPS; ranking formula uses CapacityTPS
- Critical violations (overfill, zombie fill) zero the composite_score, preventing selection

---

## 5. gRPC Control Plane

```mermaid
sequenceDiagram
    participant C as Coordinator
    participant W as Worker (×N)

    Note over C,W: Phase 1: Prepare
    C->>W: PrepareRequest {run_id, ticks[], rate}
    W-->>C: PrepareResponse {ready: true}
    Note right of W: State: idle → prepared

    Note over C,W: Phase 2: Fire (server-streaming)
    C->>W: FireRequest {run_id, fire_at_unix_ns, contestant_endpoint}
    Note right of W: Spawn 200 bot goroutines<br/>Spin-wait until fire_at
    Note right of W: State: prepared → firing
    loop Until all ticks dispatched
        W-->>C: TelemetryEvent (stream)
    end
    Note right of W: State: firing → done
    W-->>C: Stream closes (EOF)

    Note over C,W: During Fire: Rate Control (repeats each ramp interval)
    loop Until saturation detected
        C->>W: SetRateRequest {rate_per_sec: 2×}
        W-->>C: SetRateResponse {}
    end

    Note over C,W: Phase 3: Collect
    C->>W: CollectMetricsRequest {run_id}
    W-->>C: WorkerMetrics {p50, p90, p99, ticks_sent, ticks_acked}
```

All RPCs are defined in `internal/coordinator/proto/coordinator.proto`:
- `Prepare` — Unary RPC
- `Fire` — **Server-streaming** RPC (unary request, streaming response)
- `SetRate` — Unary RPC
- `CollectMetrics` — Unary RPC

---

## 6. Network Boundaries

```mermaid
graph TB
    subgraph "External (Internet-Facing)"
        ALB["Application Load Balancer"]
    end

    subgraph "AWS Private Subnet"
        subgraph "EC2 worker-0 (t3.micro)"
            API["API Server :8081"]
            COORD["Coordinator (in-process)"]
            BOT0["Worker-0 botworker :9090"]
            API --> COORD
            COORD --> BOT0
        end

        subgraph "EC2 Worker Nodes (t3.micro each)"
            W1["Worker-1 :9090"]
            Wn["Worker-N :9090"]
        end

        subgraph "ECS Fargate"
            LB["Leaderboard :8080"]
            ING["Ingester"]
        end

        RP[("EC2 t3.micro<br/>Redpanda :9092")]
        TSDB[("EC2 t3.micro<br/>TimescaleDB :5432<br/>(Docker container)")]

        ALB -->|":8081"| API
        ALB -->|":8080"| LB
        COORD -->|"gRPC :9090"| W1
        COORD -->|"gRPC :9090"| Wn
        COORD -->|"kafka :9092"| RP
        ING -->|"kafka :9092"| RP
        ING -->|"pg :5432"| TSDB
        LB -->|"pg :5432"| TSDB
        API -->|"pg :5432"| TSDB
    end

    subgraph "AWS Managed"
        S3[("S3 Bucket")]
        SQS[("SQS Queue")]
        Lambda["Lambda Compiler"]

        API -->|"upload/download"| S3
        API -->|"enqueue"| SQS
        SQS -->|"trigger"| Lambda
        Lambda -->|"upload binary"| S3
        Lambda -->|"webhook"| API
    end

    subgraph "Isolated Network (exchange-bench-internal)"
        BOT0 -->|"WebSocket :8080"| CONTESTANT["Contestant Container"]
        W1 -->|"WebSocket :8080"| CONTESTANT
        Wn -->|"WebSocket :8080"| CONTESTANT
    end

    CONTESTANT -.-x|"❌ No outbound"| EXT(["Internet"])

    style CONTESTANT fill:#ff6b6b,stroke:#c92a2a,color:#fff
    style EXT fill:#868e96,stroke:#495057,color:#fff
    style Lambda fill:#f59f00,stroke:#e67700,color:#fff
    style S3 fill:#1971c2,stroke:#1864ab,color:#fff
    style SQS fill:#1971c2,stroke:#1864ab,color:#fff
```

| Boundary | Components | Protocol |
|---|---|---|
| **External** | ALB → API, ALB → Leaderboard | HTTP |
| **Internal** | Coordinator → Workers | gRPC |
| **Internal** | Coordinator → Redpanda, Ingester → Redpanda | Kafka (:9092) |
| **Internal** | Ingester/Leaderboard/API → TimescaleDB | PostgreSQL |
| **AWS Managed** | API ↔ S3, API → SQS, Lambda → API | HTTPS |
| **Isolated** | Workers → Contestant | WebSocket (no outbound gateway) |

---

## 7. Security Layers

```mermaid
graph TD
    subgraph "Layer 1: Upload Validation"
        A1["Extension whitelist (.cpp .rs .go .py)"]
        A2["1MB file size limit"]
        A3["Single source file only"]
    end

    subgraph "Layer 2: Lambda Compilation Sandbox"
        B1["AWS Lambda managed isolation"]
        B2["3 GB RAM, 15-min max timeout"]
        B3["No outbound network by default"]
        B4["Ephemeral /tmp (512 MB)"]
        B5["50MB binary size cap (enforced in code)"]
        B6["Source uploaded to S3; binary returned via S3"]
    end

    subgraph "Layer 3: Runtime Docker Sandbox"
        C1["--network=exchange-bench-internal<br/>(no default gateway)"]
        C2["--read-only rootfs"]
        C3["--cap-drop=ALL"]
        C4["--no-new-privileges"]
        C5["--memory=512m, --cpus=2"]
        C6["--tmpfs /tmp:64m,noexec,nosuid"]
        C7["seccomp profile"]
    end

    subgraph "Layer 4: Seccomp (contestant.json)"
        D1["❌ mount / umount2 / pivot_root"]
        D2["❌ unshare / setns"]
        D3["❌ ptrace"]
        D4["❌ init_module / finit_module / delete_module"]
        D5["❌ kexec_load / kexec_file_load"]
    end

    A1 --> B1
    B1 --> C1
    C7 --> D1

    style A1 fill:#4c6ef5,stroke:#364fc7,color:#fff
    style B1 fill:#f59f00,stroke:#e67700,color:#fff
    style C1 fill:#f76707,stroke:#d9480f,color:#fff
    style D1 fill:#ff6b6b,stroke:#c92a2a,color:#fff
```

---

## 8. Database Schema

```mermaid
erDiagram
    telemetry_events {
        timestamptz time PK
        text run_id
        text submission_id
        text order_id
        bigint intended_at_ns
        bigint received_at_ns
        bigint latency_us
        boolean acked
        text violation
    }

    run_scores {
        text submission_id PK
        text team_id
        int attempt
        text run_id
        text language
        bigint ticks_sent
        bigint ticks_acked
        float peak_tps
        float capacity_tps
        bigint p50_us
        bigint p90_us
        bigint p99_us
        float correctness
        float composite_score
        boolean critical_flag
        timestamptz completed_at
    }

    telemetry_events }o--o{ run_scores : "submission_id (logical link, no FK)"
```

- `telemetry_events` is a **TimescaleDB hypertable** partitioned by `time`
- `run_scores` is a regular PostgreSQL table with `submission_id` as primary key
- `latency_us` is computed as `(received_at_ns - intended_at_ns) / 1000`
- Index: `idx_telemetry_submission ON telemetry_events (submission_id, time DESC)`
- Index: `idx_run_scores_team ON run_scores (team_id, composite_score DESC)`

---

## 9. AWS Production Architecture

```mermaid
graph TB
    subgraph "AWS ap-south-1"
        subgraph "Public"
            ALB["Application Load Balancer<br/>HTTP :80"]
            NAT["NAT Gateway"]
        end

        subgraph "Private Subnet"
            subgraph "EC2 t3.micro — worker-0"
                API["API Server Container :8081"]
                BOT0["Worker-0 Container :9090"]
            end

            subgraph "EC2 t3.micro — worker-1..N"
                BOTN["Worker-N Container :9090"]
            end

            subgraph "ECS Fargate"
                LB["Leaderboard Task :8080<br/>512 CPU / 1024 MB"]
                ING["Ingester Task<br/>512 CPU / 1024 MB"]
            end

            subgraph "EC2 t3.micro — data"
                RP["Redpanda<br/>(Docker) :9092"]
                TSDB["TimescaleDB<br/>(Docker) :5432"]
            end
        end

        subgraph "AWS Managed Services"
            S3["S3 Bucket<br/>exchange-bench-artifacts-*"]
            SQS["SQS Queue<br/>exchange-bench-compiler-jobs"]
            Lambda["Lambda Function<br/>exchange-bench-lambda-compiler<br/>3 GB RAM | Go/C++/Rust/Python"]
            ECR["ECR<br/>api / worker / leaderboard<br/>ingester / lambda-compiler<br/>contestant"]
        end
    end

    ALB -->|":8081"| API
    ALB -->|":8080"| LB
    API --> BOT0
    API -->|"gRPC"| BOTN
    API --> S3
    API --> SQS
    SQS --> Lambda
    Lambda --> S3
    Lambda -->|"webhook"| API
    BOT0 --> S3
    BOTN --> S3
    ING --> RP
    ING --> TSDB
    LB --> TSDB
    API --> TSDB

    style ALB fill:#4c6ef5,stroke:#364fc7,color:#fff
    style Lambda fill:#f59f00,stroke:#e67700,color:#fff
    style S3 fill:#1971c2,stroke:#1864ab,color:#fff
    style SQS fill:#1971c2,stroke:#1864ab,color:#fff
    style RP fill:#7950f2,stroke:#5f3dc4,color:#fff
    style TSDB fill:#20c997,stroke:#0b7285,color:#fff
```

| Component | What runs it | Instance/Config |
|---|---|---|
| API Server | EC2 Docker container | `t3.micro` (worker-0) |
| Worker-0 botworker | EC2 Docker container | `t3.micro` (worker-0, same host as API) |
| Worker-1..N | EC2 Docker container | `t3.micro` per worker |
| Leaderboard | ECS Fargate | 512 CPU / 1024 MB |
| Ingester | ECS Fargate | 512 CPU / 1024 MB |
| Lambda Compiler | AWS Lambda | 3 GB RAM, custom image |
| Redpanda | EC2 Docker container | `t3.micro` |
| TimescaleDB | EC2 Docker container | `t3.micro` |
| Artifacts + Binaries | S3 | `exchange-bench-artifacts-*` |
| Compilation Jobs | SQS | `exchange-bench-compiler-jobs` |
| Container Images | ECR | 6 repositories |
