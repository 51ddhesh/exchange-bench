package orderbook

type nodeRef struct {
	n   *node
	lvl *level
}

type book struct {
	// bids
	bids    map[int64]*level
	bestBid int64

	// asks
	asks    map[int64]*level
	bestAsk int64

	// orders
	orders map[string]nodeRef
}

func NewBook() Book {
	return &book{
		bids:   make(map[int64]*level),
		asks:   make(map[int64]*level),
		orders: make(map[string]nodeRef),
	}
}
