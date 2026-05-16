package orderbook

// standard price-time priority reference orderbook
type Book interface {
	Add(o Order) (fills []Fill, rests bool)
	Cancel(OrderID string) CancelResult
	BestBid() int64
	BestAsk() int64
	Depth(n int) (bids, asks [][2]int64)
	Reset()
}
