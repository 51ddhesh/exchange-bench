`# Changelog

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
`