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
