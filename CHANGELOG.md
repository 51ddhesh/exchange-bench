# Changelog

All notable changes to this project are documented here. 

## May 16, 2026

### Added 
- [types](./internal/orderbook/types.go): Contains the generic types used. 
- [orderbook](./internal/orderbook/book.go): Reference price-time priority orderbook.
- [price level](./internal/orderbook/level.go): all resting orders at a single price level in FIFO order. 
- [matching engine](./internal/orderbook/engine.go): matching engine - matches the buy and sell orders.


## May 17th, 2026

### Added
- [orderbook test](./internal/orderbook/book_test.go): tests the orderbook.
  - TestLimitBuyMatchesSell: Basic limit order matching (buy vs sell at same price)
  - TestPriceTimePriority: FIFO ordering at same price level
  - TestPartialFill: Taker partially fills against resting order
  - TestCancelReducesBestBid: Cancel updates best bid after removal
  - TestCancelNotFound: Cancel returns error for non-existent order
  - TestCancelFilledOrder: Cannot cancel fully filled order
  - TestMarketOrderIOC: Market sell fills partial liquidity, remainder cancelled
  - TestMarketOrderNoLiquidity: Market order returns empty when book empty
  - TestMultiLevelSweep: Taker sweeps through multiple price levels
  - TestNoFillAfterCancel: Cancelled order never produces fill (zombie fill protection)
  - TestReset: Reset clears all orders and updates best bid/ask to 0
  - TestDepth: Returns n price levels per side in price priority order
  - TestBestAskUpdatesAfterTrade: Best ask updates after consuming best ask
  - TestBuyLimitCrossesAsk: Buy limit order executes at maker's ask price
  - TestSellLimitCrossesBid: Sell limit order executes at maker's bid price
  - TestMarketBuyOrder: Market buy sweeps multiple ask levels
  - TestMultipleMakersSamePrice: Multiple orders at same price fill in FIFO order
  - TestTakerRestsMakersFullyFilled: Taker with remaining qty rests after full maker fill
  - TestBestBidUpdatesAfterTrade: Best bid updates after consuming best bid
  - TestCancelUpdatesBestAsk: Cancel updates best ask after removal
  - TestDepthNLessThanAvailable: Depth returns min(n, available levels)
  - TestMultipleTradingRounds: Sequential trades maintain correct book state
  - TestCancelPartiallyFilledOrder: Can cancel partially filled order
  - TestEmptyDepth: Empty book returns empty depth arrays
  - TestMarketOrderSweepsEntireBook: Large market order consumes all liquidity
  - TestNoTradeWhenPricesDontCross: Limit order rests when prices don't cross

### Fixed
- [matching engine](./internal/orderbook/engine.go): Fixed the `rests` return value to correctly indicate whether the taker has remaining quantity after matching, not whether any maker has remaining quantity. This was causing TestMultiLevelSweep to fail when a taker fully fills multiple maker levels.


## May 20, 2026

### Added
- [arena allocator](./internal/orderbook/engine.go): Pre-allocated slab for `Order` structs, eliminating heap allocation and GC pressure in the matching hot path.
- [node pool](./internal/orderbook/engine.go): Pre-allocated slab for doubly-linked list nodes, removing per-order heap allocation when resting orders on the book.

### Changed
- [types](./internal/orderbook/types.go): `Order.ID`, `Fill.MakerOrderID`, and `Fill.TakerOrderID` changed from `string` to `uint64` for compactness and faster map lookups. `Order.ArrivedAt` changed from `time.Time` to `int64` (UnixNano) to remove the `time` import from the matching path.
- [engine](./internal/orderbook/engine.go): `book` struct now embeds arena + node pool. `Add()` allocates orders from the arena instead of stack-copying. `rest()` allocates nodes from the pool instead of heap-allocating. `Reset()` rewinds both allocators. `Cancel()` accepts `uint64`. `newBook()` made unexported; `NewBook()` wraps it with `defaultCapacity` (1M).
- [level](./internal/orderbook/level.go): `push()` now takes a pre-allocated `*node` instead of creating one internally — allocation moved to the caller (node pool).
- [book](./internal/orderbook/book.go): `Cancel(orderID string)` → `Cancel(orderID uint64)` to match the new ID scheme.


## May 21, 2026

### Added
- [sequencer](./internal/orderbook/sequencer.go): New layer mapping external string order IDs to internal `uint64` IDs, stamping `ArrivedAt` as `time.Now().UnixNano()`, and forwarding orders to the engine. Uses atomic increment for ID assignment and RWMutex for concurrent map access.
- [Engine actor](./internal/orderbook/engine.go): Wraps the book in a channel-based serialization model. `Run()` starts a dedicated goroutine pinned to an OS thread via `runtime.LockOSThread()` for CPU affinity. `Submit()` and `CancelOrder()` are safe to call from any goroutine.

### Changed
- [engine](./internal/orderbook/engine.go): `matchLimit()` and `sweep()` simplified — removed `makerRested` return value that was no longer consumed by any caller.


## May 22, 2026

### Added
- [code review suggestions](./SUGGESTIONS.md): Documents 7 observations including bubble sort in `collectDepth`, redundant `min64` helper, arena panic behaviour, O(n) `findBest` scan, test coverage gap for market buy with partial liquidity, hardcoded channel buffer, and unused exported `NewBook()`.

### Changed
- [tests](./internal/orderbook/book_test.go): All tests migrated from `NewBook()` to `newBook(1024)`. Order IDs changed from strings to `uint64`. Added missing `rests` assertion in `TestPartialFill`. Removed redundant comments and secondary assertions (total filled qty checks, intermediate best bid/ask checks).


## May 25, 2026

### Added
- [tick](./internal/workload/tick.go): Single instruction in the deterministic workload sequence.
  - `TickType` enum (`TickAdd = 1`, `TickCancel = 2`) distinguishing ADD and CANCEL instructions.
  - `Tick` struct with fields: `Type`, `OrderID` (string — e.g. `"o1"`, `"o2"` — converted to `uint64` by the sequencer layer), `Side` (`'B'`/`'S'`, zero for CANCEL), `OrdType` (`'L'`/`'M'`, zero for CANCEL), `Price` (fixed-point ×10000, zero for market orders and CANCEL), `Qty` (zero for CANCEL).
  - Cancel ticks have zero-valued payload fields; the protocol layer and validator rely on this invariant.
- [generator](./internal/workload/generator.go): Deterministic but statistically-invariant market regime generator.
  - Market structure constants: `tickSize = 100` (×10000 scale), `baseMid = 10_000_000`, `midFloor = 1_000_000`, `midSigma = 0.0002` (per-tick log-normal volatility), `spreadTicks = 10` (half-width of limit price band around mid), `qtyMin = 1`, `qtyMax = 100`.
  - **5 market regimes** with Gaussian blending for smooth transitions:
    - *Warmup* (center=0.05): 90% limit, 10% cancel — establishes baseline book depth.
    - *Normal* (center=0.30): 60% limit, 15% cancel — mixed order flow, moderate crossing.
    - *MarketMaking* (center=0.55): 80% limit, 30% cancel — tight spread, partial fills, FIFO queue stress.
    - *CancelStorm* (center=0.75): 30% limit, 70% cancel — book integrity under mass cancellation, zombie-fill risk.
    - *Spike* (center=0.92): 60% limit, 15% cancel — peak throughput, lock contention, queue depth pressure.
  - `Generate(seed, totalTicks)` returns a deterministic `[]Tick` sequence.
  - Box-Muller transform drives a log-normal mid-price walk, clamped to `midFloor`.
  - `blend()` computes Gaussian-weighted convex combination of regime parameters at each tick index, plus independent ±0.05 noise, clamped to [0, 1].
  - `quantize()` rounds price down to nearest `tickSize` multiple (minimum `tickSize`).
  - Swap-remove selection from a running resting pool for cancel targets.
- [generator tests](./internal/workload/generator_test.go): 257 lines covering deterministic and statistical invariants.
  - **Reproducibility**: `TestDeterminism` — identical (seed, totalTicks) must produce byte-identical output (critical platform guarantee). `TestDifferentSeedsDifferentOutput` — distinct seeds produce distinct sequences.
  - **Structural invariants**: `TestTotalTickCount` (output length equals `totalTicks`), `TestAddFieldsPopulated` (all ADD ticks have valid Side/OrdType/Qty), `TestCancelFieldsClean` (CANCEL ticks have zero-valued payload fields).
  - **Price invariants**: `TestLimitPricePositive` (limit orders have Price > 0), `TestMarketOrderPriceIsZero` (market orders have Price == 0).
  - **Cancel correctness**: `TestCancelReferencesOnlyPriorAdds` (every CANCEL target was previously ADDed), `TestNoCancelBeforeItsAdd` (ADD appears at strictly lower index than its CANCEL), `TestCancelPoolInvariant` (CANCEL target is currently resting; no double-cancel).
  - **Statistical distribution**: `TestCancelFractionInRange` (overall cancel fraction within [0.05, 0.75]), `TestSlidingWindowNonDetectability` (anti-fingerprinting — Gaussian blend keeps all 1K-tick window-to-window cancel-fraction deltas below 0.15, proving no sequential phase boundaries).


## May 26, 2026

### Added
- [protocol types](./internal/protocol/protocol.go): `Request` and `Response` data types, `ResponseType` enum (`RespACK`, `RespFILL`, `RespREJ`), and `ParseError` sentinel for non-fatal parse failures in the contestant wire protocol.
- [protocol writer](./internal/protocol/writer.go): `WriteTick` serialises a `workload.Tick` to ASCII line protocol (`ADD`/`CAN`). `formatPrice` converts ×10000 int64 to fixed-4-decimal string without float64.
- [protocol reader](./internal/protocol/reader.go): `ReadResponse` parses one contestant stdout line into a `Response`. `parsePrice` inverts `formatPrice` and is robust against variable decimal precision. All parse errors are non-fatal `ParseError` values.
- [protocol tests](./internal/protocol/reader_test.go): 311 lines covering `TestReadResponse` (all legal/illegal line shapes), `TestPriceRoundTrip` (formatPrice ∘ parsePrice identity), `TestParsePriceRobust` (contestant deviations), and `TestWriteTickRoundTrip` (ADD limit/market and CAN round-trip).


## May 27, 2026

### Added
- [sandbox](./internal/runner/sandbox.go): `Sandbox` interface (`Stdin`/`Stdout`/`Kill`) decoupling the dispatch loop from process management. `dockerSandbox` production implementation creates a hardened Docker container (no network, read-only rootfs, 64 MB tmpfs, 2 CPUs, 512 MB memory, no swap, all caps dropped, no-new-privileges, seccomp) and attaches stdio.
- [runner](./internal/runner/runner.go): `Runner` struct driving the dispatch loop. `Run()` sends ticks at a configurable rate via `protocol.WriteTick`, collects responses via `collectUntilACK()` (accumulating FILLs before ACK/REJ), records latency in an HDR histogram (1µs–60s), and streams `TickResult` values to the validator. `RunMetrics` aggregates P50/P90/P99 latency, peak TPS, and tick counts. Handles coordinated omission by measuring from `IntendedAt` (ticker fire) rather than actual send time.

### Fixed
- [sandbox](./internal/runner/sandbox.go): `*bufio.Reader` does not implement `io.ReadCloser` (missing `Close()`). Wrapped it with a `bufReadCloser` struct that composes `*bufio.Reader` with the underlying `io.Closer` from the hijacked Docker attach connection.
- [runner tests](./internal/runner/runner_test.go): `TestPipeClosedMidRun` hang — the test goroutine closed stdout but stopped reading stdin, causing `WriteTick` to block forever on the unbuffered `io.Pipe`. Fixed by draining remaining stdin with `io.Copy(io.Discard, ...)` after closing stdout.
- [runner](./internal/runner/runner.go): `TestParseErrorIsNonFatal` — `collectUntilACK` treated all non-EOF errors as fatal pipe errors, aborting the dispatch loop. Added `protocol.ParseError` type to distinguish malformed-line errors from scanner I/O errors. Parse errors now return nil error from `collectUntilACK`, letting the loop continue to the next tick.

### Changed
- [go.mod](./go.mod): Added `github.com/HdrHistogram/hdrhistogram-go` (latency histograms) and `github.com/docker/docker` (container sandbox) with all transitive dependencies.
- [go.sum](./go.sum): Checksums for the above dependency tree (103 lines).


## May 28, 2026

### Added
- [seccomp profile](./deployments/docker/seccomp/contestant.json): Contestant seccomp profile blocking network (socket, connect, bind, etc.) and process-control syscalls (fork, clone, execve). Default-allow with explicit deny list for safety-critical kernel interfaces.
- [sandbox CLI refactor](./internal/runner/sandbox.go): `StartSandbox` now invokes `docker run` via `os/exec` instead of the Docker SDK. Drops the `bufReadCloser` wrapper (OS pipes natively satisfy `io.ReadCloser`) and removes all Docker SDK imports and dependencies.
- [contestant binary](./cmd/contestant/main.go): Reference contestant implementation backed by the matching engine. Reads `ADD`/`CAN` from stdin, writes `FILL`/`ACK`/`REJ` to stdout. Any correct platform evaluation must score it at 100%.
- [validator](./internal/validator/validator.go): `Validator` replays each `TickResult` through an independent reference engine and compares contestant output against reference truth. Detects `OVERFILL`, `UNDERFILL`, `PRICE_TIME_PRIORITY`, `WRONG_EXEC_PRICE`, `ZOMBIE_FILL`, and `CANCEL_MISMATCH` violations. Owns its own `Engine` and `Sequencer` — no shared state with the runner.
- [validator tests](./internal/validator/validator_test.go): 288 lines covering 12 test cases: correct no-fill, correct with fill, correct cancel, overfill, underfill, price-time priority, wrong exec price, zombie fill, cancel mismatch (OK→REJ and not-found→ACK), multi-level sweep (correct and wrong order), market order IOC, and partial fill.
- [agent CLI](./cmd/agent/main.go): `cmd/agent` — evaluation entry point. Generates a workload via `workload.Generate`, starts a sandboxed contestant via `runner.StartSandbox`, dispatches ticks via `runner.Run`, scores via `validator.Consume`, and prints a result summary with latency percentiles, tick counts, correctness percentage, and violation breakdown.

### Changed
- [go.mod](./go.mod): Removed `github.com/docker/docker` and all 20+ transitive Docker SDK dependencies. Only `github.com/HdrHistogram/hdrhistogram-go` remains.
- [go.sum](./go.sum): Reduced from 103 lines to 14 lines after dependency cleanup.

