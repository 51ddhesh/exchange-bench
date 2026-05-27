# exchange-bench

Distributed exchange benchmarking suite which evaluates (WIP) contestant trading algorithms against a deterministic, statistically-invariant market simulator.


## Packages

- **orderbook**: Reference matching engine. Price-time priority FIFO book with arena allocator (zero-alloc hot path), node pool, and channel-based actor serialisation via `Engine.Run()`.

- **workload**: Deterministic market regime generator. Produces a `[]Tick` sequence from a seed using a log-normal mid-price walk and 5 Gaussian-blended regimes (Warmup, Normal, MarketMaking, CancelStorm, Spike). Statistically invariant; same seed always produces identical output.

- **protocol**: Typed wire protocol for contestant communication. `WriteTick` serialises ADD/CAN ticks to ASCII lines; `ReadResponse` parses contestant ACK/FILL/REJ lines. All parse errors are non-fatal.

- **runner**: Dispatch loop that sends ticks to a contestant process at a configurable rate, collects responses, and records latency via HDR histogram. Production sandbox uses a hardened Docker container (no network, read-only rootfs, capped resources, seccomp).