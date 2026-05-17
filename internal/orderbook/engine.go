package orderbook

import (
	"time"
)

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

// BOOK INTERFACE (definition in book.go)

// Places an order with price-time priority
// Market orders that cannot be fulfilled are IOC
// Returns all fills after matching
func (b *book) Add(o Order) (fills []Fill, rests bool) {
	stored := o
	stored.ArrivedAt = time.Now()

	switch stored.Type {
	case Market:
		fills = b.matchMarket(&stored)
		return fills, false

	case Limit:
		fills, _ := b.matchLimit(&stored)
		if stored.RemainingQty() > 0 {
			b.rest(&stored)
			return fills, true
		}

		return fills, false
	}

	return nil, false
}

// Remove a resting order in constant time
func (b *book) Cancel(orderID string) CancelResult {
	ref, ok := b.orders[orderID]

	if !ok {
		return CancelNotFound
	}

	ref.lvl.remove(ref.n)
	delete(b.orders, orderID)

	if ref.lvl.empty() {
		b.removeLevel(ref.n.order.Side, ref.lvl.price)
	}

	return CancelOK
}

func (b *book) BestBid() int64 {
	return b.bestBid
}

func (b *book) BestAsk() int64 {
	return b.bestAsk
}

// Returns 'n' price levels per side
// Follow price priority per side
func (b *book) Depth(n int) (bids, asks [][2]int64) {
	bids = collectDepth(b.bids, n, Buy)
	asks = collectDepth(b.asks, n, Sell)
	return
}

func (b *book) Reset() {
	b.bids = make(map[int64]*level)
	b.asks = make(map[int64]*level)
	b.orders = make(map[string]nodeRef)
	b.bestBid = 0
	b.bestAsk = 0
}

// MATCHING

// Sweep the opposite side greedily until filled or book empty
func (b *book) matchMarket(o *Order) []Fill {
	var fills []Fill

	for o.RemainingQty() > 0 {
		best, lvl := b.bestOpposite(o.Side)

		if lvl == nil {
			break // no liquidity, remainder cancelled (IOC)
		}

		f, _ := b.sweep(lvl, best, o)
		fills = append(fills, f...)

		if lvl.empty() {
			b.removeLevel(opposite(o.Side), best)
		}
	}

	return fills
}

// Match limit orders against the book as long as prices cross
func (b *book) matchLimit(o *Order) ([]Fill, bool) {
	var fills []Fill
	makerRested := false

	for o.RemainingQty() > 0 {
		best, lvl := b.bestOpposite(o.Side)

		if lvl == nil {
			break
		}

		if !pricesCross(o.Side, o.Price, best) {
			break
		}

		f, hadRemaining := b.sweep(lvl, best, o)
		fills = append(fills, f...)
		if hadRemaining {
			makerRested = true
		}

		if lvl.empty() {
			b.removeLevel(opposite(o.Side), best)
		}
	}

	return fills, makerRested
}

// Walk the FIFO list at a price level and generate fills
func (b *book) sweep(lvl *level, execPrice int64, taker *Order) ([]Fill, bool) {
	var fills []Fill
	makerRested := false

	for lvl.head != nil && taker.RemainingQty() > 0 {
		makerNode := lvl.head
		maker := makerNode.order

		qty := min64(maker.RemainingQty(), taker.RemainingQty())
		maker.FilledQty += qty
		taker.FilledQty += qty
		lvl.total -= qty

		fills = append(fills, Fill{
			MakerOrderID: maker.ID,
			TakerOrderID: taker.ID,
			ExecPrice:    execPrice,
			ExecQty:      qty,
		})

		if maker.RemainingQty() == 0 {
			lvl.head = makerNode.next
			if lvl.head != nil {
				lvl.head.prev = nil
			} else {
				lvl.tail = nil
			}
			delete(b.orders, maker.ID)
		} else {
			makerRested = true
		}
	}

	return fills, makerRested
}

// Adds partial/unfilled orders to the book
func (b *book) rest(o *Order) {
	var lvls map[int64]*level

	if o.Side == Buy {
		lvls = b.bids
	} else {
		lvls = b.asks
	}

	lvl, ok := lvls[o.Price]

	if !ok {
		lvl = newLevel(o.Price)
		lvls[o.Price] = lvl
	}

	n := lvl.push(o)
	b.orders[o.ID] = nodeRef{n: n, lvl: lvl}
	b.updateBest(o.Side, o.Price)
}

// Returns the price and level of the best resting order on the
// side opposite to the incoming order.
func (b *book) bestOpposite(side Side) (int64, *level) {
	if side == Buy {
		// taker is a buyer, so we match against asks (lowest ask first)
		if b.bestAsk == 0 {
			return 0, nil
		}
		return b.bestAsk, b.asks[b.bestAsk]
	}
	// taker is a seller, match against bids (highest bid first)
	if b.bestBid == 0 {
		return 0, nil
	}

	return b.bestBid, b.bids[b.bestBid]
}

// Reports whether the incoming limit order's price is aggressive
// enough to trade against the best resting price on the opposite side.
func pricesCross(takerSide Side, takerPrice, makerPrice int64) bool {
	if takerSide == Buy {
		return takerPrice >= makerPrice // buyer willing to pay at least ask
	}

	return takerPrice <= makerPrice // seller willing to accept at most bid
}

// Deletes an empty price level and updates bestBid/bestAsk.
func (b *book) removeLevel(side Side, price int64) {
	if side == Buy {
		delete(b.bids, price)

		if price == b.bestBid {
			b.bestBid = findBest(b.bids, Buy)
		}
	} else {
		delete(b.asks, price)

		if price == b.bestAsk {
			b.bestAsk = findBest(b.asks, Sell)
		}
	}
}

// Updates bestBid or bestAsk after a new level is added.
func (b *book) updateBest(side Side, price int64) {
	if side == Buy {
		if price > b.bestBid {
			b.bestBid = price
		}
	} else {
		if b.bestAsk == 0 || price < b.bestAsk {
			b.bestAsk = price
		}
	}
}

// Scans all active price levels to find the new best after removal.
func findBest(levels map[int64]*level, side Side) int64 {
	var best int64

	for price := range levels {
		if side == Buy {
			if price > best {
				best = price
			}
		} else {
			if best == 0 || price < best {
				best = price
			}
		}
	}

	return best
}

// collectDepth builds the Depth() response for one side.
func collectDepth(levels map[int64]*level, n int, side Side) [][2]int64 {
	result := make([][2]int64, 0, n)

	// We need sorted prices. Collect, sort, slice.
	prices := make([]int64, 0, len(levels))

	for p := range levels {
		prices = append(prices, p)
	}

	sortPrices(prices, side)

	for i, p := range prices {
		if i >= n {
			break
		}

		result = append(result, [2]int64{p, levels[p].total})
	}

	return result
}

// Sorts prices descending for bids, ascending for asks.
func sortPrices(prices []int64, side Side) {
	for i := 0; i < len(prices); i++ {
		for j := i + 1; j < len(prices); j++ {
			swap := false

			if side == Buy && prices[j] > prices[i] {
				swap = true
			}

			if side == Sell && prices[j] < prices[i] {
				swap = true
			}

			if swap {
				prices[i], prices[j] = prices[j], prices[i]
			}
		}
	}
}

func opposite(s Side) Side { return -s }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}

	return b
}
