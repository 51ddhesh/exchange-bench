package orderbook

import "testing"

func newOrder(id uint64, side Side, _type OrderType, price, qty int64) Order {
	return Order{
		ID:    id,
		Side:  side,
		Type:  _type,
		Price: price,
		Qty:   qty,
	}
}

func TestLimitBuyMatchesSell(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1005000, 10))
	fills, rests := b.Add(newOrder(2, Sell, Limit, 1005000, 10))

	if rests {
		t.Fatal("fully matched order should not rest")
	}
	if len(fills) != 1 {
		t.Fatalf("want 1 fill, got %d", len(fills))
	}
	f := fills[0]
	if f.MakerOrderID != 1 || f.TakerOrderID != 2 {
		t.Errorf("wrong maker/taker: %+v", f)
	}
	if f.ExecQty != 10 {
		t.Errorf("want qty 10, got %d", f.ExecQty)
	}
	if f.ExecPrice != 1005000 {
		t.Errorf("want price 1005000, got %d", f.ExecPrice)
	}
}

// PRICE TIME PRIORITY

func TestPriceTimePriority(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10)) // arrives first
	b.Add(newOrder(2, Buy, Limit, 1000000, 10)) // same price, arrives later

	fills, _ := b.Add(newOrder(3, Sell, Limit, 1000000, 10))

	if len(fills) != 1 {
		t.Fatalf("want 1 fill, got %d", len(fills))
	}
	if fills[0].MakerOrderID != 1 {
		t.Errorf("time priority violated: expected maker 1, got %d", fills[0].MakerOrderID)
	}
}

// PARTIAL FILL

func TestPartialFill(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	fills, rests := b.Add(newOrder(2, Sell, Limit, 1000000, 6))

	if rests {
		t.Fatal("sell order should be fully filled")
	}
	if len(fills) != 1 || fills[0].ExecQty != 6 {
		t.Errorf("want fill qty 6, got %+v", fills)
	}
	if b.BestBid() != 1000000 {
		t.Errorf("best bid should still be 1000000, got %d", b.BestBid())
	}
}

// CANCEL

func TestCancelReducesBestBid(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1010000, 5))
	b.Add(newOrder(2, Buy, Limit, 1000000, 5))

	if b.BestBid() != 1010000 {
		t.Fatalf("want best bid 1010000, got %d", b.BestBid())
	}
	b.Cancel(1)
	if b.BestBid() != 1000000 {
		t.Errorf("after cancel best bid should be 1000000, got %d", b.BestBid())
	}
}

func TestCancelNotFound(t *testing.T) {
	b := newBook(1024)
	if result := b.Cancel(999); result != CancelNotFound {
		t.Errorf("want CancelNotFound, got %d", result)
	}
}

func TestCancelFilledOrder(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 5))
	b.Add(newOrder(2, Sell, Limit, 1000000, 5)) // fully fills 1

	if result := b.Cancel(1); result != CancelNotFound {
		t.Errorf("fully filled order should not be cancellable, got %d", result)
	}
}

// MARKET ORDER

func TestMarketOrderIOC(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 3))

	fills, rests := b.Add(newOrder(2, Sell, Market, 0, 10))

	if rests {
		t.Fatal("market order remainder must not rest (IOC)")
	}
	if len(fills) != 1 || fills[0].ExecQty != 3 {
		t.Errorf("want fill qty 3, got %+v", fills)
	}
}

func TestMarketOrderNoLiquidity(t *testing.T) {
	b := newBook(1024)
	fills, rests := b.Add(newOrder(1, Buy, Market, 0, 10))

	if rests {
		t.Fatal("market order must not rest when book is empty")
	}
	if len(fills) != 0 {
		t.Errorf("want no fills, got %+v", fills)
	}
}

// MULTI LEVEL SWEEP

func TestMultiLevelSweep(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1010000, 5)) // best bid
	b.Add(newOrder(2, Buy, Limit, 1000000, 5)) // second best

	fills, rests := b.Add(newOrder(3, Sell, Limit, 1000000, 8))

	if rests {
		t.Fatal("taker should be fully filled")
	}
	if len(fills) != 2 {
		t.Fatalf("want 2 fills, got %d", len(fills))
	}
	if fills[0].MakerOrderID != 1 || fills[0].ExecPrice != 1010000 {
		t.Errorf("first fill should be against best bid: %+v", fills[0])
	}
	if fills[1].MakerOrderID != 2 || fills[1].ExecQty != 3 {
		t.Errorf("second fill should take 3 from order 2: %+v", fills[1])
	}
}

// ZOMBIE FILL

func TestNoFillAfterCancel(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	b.Cancel(1)

	fills, _ := b.Add(newOrder(2, Sell, Limit, 1000000, 10))
	if len(fills) != 0 {
		t.Errorf("cancelled order must not fill, got %+v", fills)
	}
}

// RESET

func TestReset(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	b.Reset()

	if b.BestBid() != 0 {
		t.Errorf("after reset best bid should be 0, got %d", b.BestBid())
	}
	fills, _ := b.Add(newOrder(2, Sell, Limit, 1000000, 5))
	if len(fills) != 0 {
		t.Errorf("after reset no resting orders should exist, got %+v", fills)
	}
}

// DEPTH

func TestDepth(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	b.Add(newOrder(2, Buy, Limit, 1005000, 5))
	b.Add(newOrder(3, Sell, Limit, 1010000, 8))
	b.Add(newOrder(4, Sell, Limit, 1015000, 3))

	bids, asks := b.Depth(5)

	if len(bids) != 2 {
		t.Fatalf("want 2 bid levels, got %d", len(bids))
	}
	if bids[0][0] != 1005000 || bids[0][1] != 5 {
		t.Errorf("best bid should be 1005000/5, got %v", bids[0])
	}
	if bids[1][0] != 1000000 || bids[1][1] != 10 {
		t.Errorf("second bid should be 1000000/10, got %v", bids[1])
	}
	if len(asks) != 2 {
		t.Fatalf("want 2 ask levels, got %d", len(asks))
	}
	if asks[0][0] != 1010000 || asks[0][1] != 8 {
		t.Errorf("best ask should be 1010000/8, got %v", asks[0])
	}
	if asks[1][0] != 1015000 || asks[1][1] != 3 {
		t.Errorf("second ask should be 1015000/3, got %v", asks[1])
	}
}

// BEST ASK UPDATES

func TestBestAskUpdatesAfterTrade(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Sell, Limit, 1000000, 5))
	b.Add(newOrder(2, Sell, Limit, 1005000, 5))

	if b.BestAsk() != 1000000 {
		t.Fatalf("initial best ask should be 1000000, got %d", b.BestAsk())
	}
	b.Add(newOrder(3, Buy, Limit, 1000000, 5))
	if b.BestAsk() != 1005000 {
		t.Errorf("after filling best ask, should be 1005000, got %d", b.BestAsk())
	}
}

// PRICE CROSSING

func TestBuyLimitCrossesAsk(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Sell, Limit, 1000000, 10))
	fills, _ := b.Add(newOrder(2, Buy, Limit, 1005000, 5))

	if len(fills) != 1 {
		t.Fatalf("want 1 fill, got %d", len(fills))
	}
	if fills[0].ExecPrice != 1000000 {
		t.Errorf("exec price should be maker's ask (1000000), got %d", fills[0].ExecPrice)
	}
}

func TestSellLimitCrossesBid(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1005000, 10))
	fills, _ := b.Add(newOrder(2, Sell, Limit, 1000000, 5))

	if len(fills) != 1 {
		t.Fatalf("want 1 fill, got %d", len(fills))
	}
	if fills[0].ExecPrice != 1005000 {
		t.Errorf("exec price should be maker's bid (1005000), got %d", fills[0].ExecPrice)
	}
}

// MARKET BUY

func TestMarketBuyOrder(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Sell, Limit, 1000000, 3))
	b.Add(newOrder(2, Sell, Limit, 1005000, 5))

	fills, rests := b.Add(newOrder(3, Buy, Market, 0, 10))

	if rests {
		t.Fatal("market order must not rest (IOC)")
	}
	if len(fills) != 2 {
		t.Fatalf("want 2 fills, got %d", len(fills))
	}
	if fills[0].ExecPrice != 1000000 || fills[0].ExecQty != 3 {
		t.Errorf("first fill: %+v", fills[0])
	}
	if fills[1].ExecPrice != 1005000 || fills[1].ExecQty != 5 {
		t.Errorf("second fill: %+v", fills[1])
	}
}

// MULTIPLE MAKERS SAME PRICE

func TestMultipleMakersSamePrice(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 5))
	b.Add(newOrder(2, Buy, Limit, 1000000, 5))

	fills, _ := b.Add(newOrder(3, Sell, Limit, 1000000, 8))

	if len(fills) != 2 {
		t.Fatalf("want 2 fills, got %d", len(fills))
	}
	if fills[0].MakerOrderID != 1 || fills[0].ExecQty != 5 {
		t.Errorf("first maker fully filled: %+v", fills[0])
	}
	if fills[1].MakerOrderID != 2 || fills[1].ExecQty != 3 {
		t.Errorf("second maker partially filled: %+v", fills[1])
	}
}

// TAKER RESTS AFTER EXHAUSTING MAKERS

func TestTakerRestsMakersFullyFilled(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 5))
	fills, rests := b.Add(newOrder(2, Sell, Limit, 1000000, 10))

	if !rests {
		t.Fatal("taker should rest with remaining 5")
	}
	if len(fills) != 1 || fills[0].ExecQty != 5 {
		t.Errorf("maker fully filled: %+v", fills)
	}
	if b.BestBid() != 0 {
		t.Errorf("after trade best bid should be 0, got %d", b.BestBid())
	}
}

// BEST BID UPDATES AFTER TRADE

func TestBestBidUpdatesAfterTrade(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 5))
	b.Add(newOrder(2, Buy, Limit, 1005000, 5))

	if b.BestBid() != 1005000 {
		t.Fatalf("initial best bid should be 1005000, got %d", b.BestBid())
	}
	b.Add(newOrder(3, Sell, Limit, 1005000, 5))
	if b.BestBid() != 1000000 {
		t.Errorf("after filling best bid, should be 1000000, got %d", b.BestBid())
	}
}

// CANCEL UPDATES BEST ASK

func TestCancelUpdatesBestAsk(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Sell, Limit, 1000000, 5))
	b.Add(newOrder(2, Sell, Limit, 1005000, 5))

	if b.BestAsk() != 1000000 {
		t.Fatalf("initial best ask should be 1000000, got %d", b.BestAsk())
	}
	b.Cancel(1)
	if b.BestAsk() != 1005000 {
		t.Errorf("after cancelling best ask, should be 1005000, got %d", b.BestAsk())
	}
}

// DEPTH N LESS THAN AVAILABLE

func TestDepthNLessThanAvailable(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	b.Add(newOrder(2, Buy, Limit, 1005000, 5))
	b.Add(newOrder(3, Buy, Limit, 1010000, 3))

	bids, _ := b.Depth(2)

	if len(bids) != 2 {
		t.Fatalf("want 2 levels, got %d", len(bids))
	}
	if bids[0][0] != 1010000 || bids[0][1] != 3 {
		t.Errorf("best bid should be 1010000/3, got %v", bids[0])
	}
	if bids[1][0] != 1005000 || bids[1][1] != 5 {
		t.Errorf("second bid should be 1005000/5, got %v", bids[1])
	}
}

// MULTIPLE TRADING ROUNDS

func TestMultipleTradingRounds(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))

	fills1, _ := b.Add(newOrder(2, Sell, Limit, 1000000, 5))
	if len(fills1) != 1 || fills1[0].ExecQty != 5 {
		t.Fatalf("first round: %+v", fills1)
	}

	b.Add(newOrder(3, Buy, Limit, 1005000, 3))

	fills2, _ := b.Add(newOrder(4, Sell, Limit, 1000000, 8))
	if len(fills2) != 2 {
		t.Fatalf("second round should have 2 fills, got %d", len(fills2))
	}
	if fills2[0].MakerOrderID != 3 || fills2[0].ExecQty != 3 {
		t.Errorf("first fill should be against order 3 (best bid): %+v", fills2[0])
	}
	if fills2[1].MakerOrderID != 1 || fills2[1].ExecQty != 5 {
		t.Errorf("second fill should be against order 1: %+v", fills2[1])
	}
}

// CANCEL PARTIALLY FILLED ORDER

func TestCancelPartiallyFilledOrder(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	b.Add(newOrder(2, Sell, Limit, 1000000, 6))

	if result := b.Cancel(1); result != CancelOK {
		t.Errorf("partially filled order should be cancellable, got %d", result)
	}
	if b.BestBid() != 0 {
		t.Errorf("after cancel best bid should be 0, got %d", b.BestBid())
	}
}

// EMPTY DEPTH

func TestEmptyDepth(t *testing.T) {
	b := newBook(1024)
	bids, asks := b.Depth(5)
	if len(bids) != 0 || len(asks) != 0 {
		t.Errorf("empty book should have empty depth, got bids=%d asks=%d", len(bids), len(asks))
	}
}

// MARKET ORDER SWEEPS ENTIRE BOOK

func TestMarketOrderSweepsEntireBook(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Sell, Limit, 1000000, 3))
	b.Add(newOrder(2, Sell, Limit, 1005000, 5))
	b.Add(newOrder(3, Sell, Limit, 1010000, 2))

	fills, rests := b.Add(newOrder(4, Buy, Market, 0, 20))

	if rests {
		t.Fatal("market order should not rest (IOC)")
	}
	if len(fills) != 3 {
		t.Fatalf("want 3 fills, got %d", len(fills))
	}
	if fills[0].ExecQty != 3 || fills[1].ExecQty != 5 || fills[2].ExecQty != 2 {
		t.Errorf("fill quantities mismatch: %+v", fills)
	}
}

// NO TRADE WHEN PRICES DON'T CROSS

func TestNoTradeWhenPricesDontCross(t *testing.T) {
	b := newBook(1024)
	b.Add(newOrder(1, Buy, Limit, 1000000, 10))
	fills, rests := b.Add(newOrder(2, Sell, Limit, 1005000, 5))

	if len(fills) != 0 {
		t.Errorf("no trades should occur, got %+v", fills)
	}
	if !rests {
		t.Fatal("seller should rest at 1005000")
	}
	if b.BestBid() != 1000000 {
		t.Errorf("best bid should be 1000000, got %d", b.BestBid())
	}
	if b.BestAsk() != 1005000 {
		t.Errorf("best ask should be 1005000, got %d", b.BestAsk())
	}
}
