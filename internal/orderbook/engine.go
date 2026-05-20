package orderbook

import (
	"context"
	"runtime"
)

// Arena allocator
// Eliminates per-order heap allocation and GC overhead
// ! NOT THREAD SAFE
type arena struct {
	slab   []Order
	cursor int
}

func newArena(capacity int) arena {
	return arena{slab: make([]Order, capacity)}
}

// Returns a pointer to the next free slot
// Panics on arena exhaustion
func (a *arena) alloc() *Order {
	if a.cursor >= len(a.slab) {
		panic("[orderbook/engine]: arena exhausted")
	}

	o := &a.slab[a.cursor]
	a.cursor++

	return o
}

func (a *arena) reset() {
	a.cursor = 0
}

// Pre-allocated slab for linked list nodes
// same semantics as arena allocator
type nodePool struct {
	slab   []node
	cursor int
}

func newNodePool(capacity int) nodePool {
	return nodePool{slab: make([]node, capacity)}
}

func (p *nodePool) alloc() *node {
	if p.cursor >= len(p.slab) {
		panic("[orderbook/engine]: node pool exhausted")
	}

	n := &p.slab[p.cursor]
	p.cursor++

	return n
}

func (p *nodePool) reset() {
	p.cursor = 0
}

// Locate a node in constant time
type nodeRef struct {
	n   *node
	lvl *level
}

const defaultCapacity = 1_000_000

type book struct {
	bids    map[int64]*level
	bestBid int64

	asks    map[int64]*level
	bestAsk int64

	orders map[uint64]nodeRef

	arena arena
	nodes nodePool
}

func newBook(capacity int) *book {
	return &book{
		bids:   make(map[int64]*level),
		asks:   make(map[int64]*level),
		orders: make(map[uint64]nodeRef, capacity),
		arena:  newArena(capacity),
		nodes:  newNodePool(capacity),
	}
}

func NewBook() Book {
	return newBook(defaultCapacity)
}

// BOOK INTERFACE
func (b *book) Add(o Order) (fills []Fill, rests bool) {
	// Claim an arena slot and copy the order in
	stored := b.arena.alloc()

	*stored = o

	switch stored.Type {
	case Market:
		return b.matchMarket(stored), false

	case Limit:
		fills = b.matchLimit(stored)
		if stored.RemainingQty() > 0 {
			b.rest(stored)
			return fills, true
		}

		return fills, false
	}

	return nil, false
}

func (b *book) Cancel(orderID uint64) CancelResult {
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

func (b *book) BestBid() int64 { return b.bestBid }
func (b *book) BestAsk() int64 { return b.bestAsk }

func (b *book) Depth(n int) (bids, asks [][2]int64) {
	bids = collectDepth(b.bids, n, Buy)
	asks = collectDepth(b.asks, n, Sell)
	return
}

func (b *book) Reset() {
	b.bids = make(map[int64]*level)
	b.asks = make(map[int64]*level)
	b.orders = make(map[uint64]nodeRef, cap(b.arena.slab))
	b.bestBid = 0
	b.bestAsk = 0
	b.arena.reset()
	b.nodes.reset()
}

// MATCHING

func (b *book) matchMarket(o *Order) []Fill {
	var fills []Fill

	for o.RemainingQty() > 0 {
		best, lvl := b.bestOpposite(o.Side)
		if lvl == nil {
			break // no liquidity
		}

		fills = append(fills, b.sweep(lvl, best, o)...)

		if lvl.empty() {
			b.removeLevel(opposite(o.Side), best)
		}
	}

	return fills
}

func (b *book) matchLimit(o *Order) []Fill {
	var fills []Fill

	for o.RemainingQty() > 0 {
		best, lvl := b.bestOpposite(o.Side)
		if lvl == nil {
			break
		}

		if !pricesCross(o.Side, o.Price, best) {
			break
		}

		fills = append(fills, b.sweep(lvl, best, o)...)

		if lvl.empty() {
			b.removeLevel(opposite(o.Side), best)
		}
	}

	return fills
}

// Walks the FIFO list at a price level, generating fills until the taker
// is filled or the level is exhausted. Exec price is the maker's resting price.
func (b *book) sweep(lvl *level, execPrice int64, taker *Order) []Fill {
	var fills []Fill
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
			// Fully filled
			lvl.head = makerNode.next
			if lvl.head != nil {
				lvl.head.prev = nil
			} else {
				lvl.tail = nil
			}

			delete(b.orders, maker.ID)
		}
	}

	return fills
}

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

	// Allocate node from pool — no heap allocation.
	n := b.nodes.alloc()
	n.order = o
	n.prev = nil
	n.next = nil

	lvl.push(n)
	b.orders[o.ID] = nodeRef{n: n, lvl: lvl}
	b.updateBest(o.Side, o.Price)
}

func (b *book) bestOpposite(side Side) (int64, *level) {
	if side == Buy {
		if b.bestAsk == 0 {
			return 0, nil
		}
		return b.bestAsk, b.asks[b.bestAsk]
	}
	if b.bestBid == 0 {
		return 0, nil
	}
	return b.bestBid, b.bids[b.bestBid]
}

func pricesCross(takerSide Side, takerPrice, makerPrice int64) bool {
	if takerSide == Buy {
		return takerPrice >= makerPrice
	}
	return takerPrice <= makerPrice
}

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

func collectDepth(levels map[int64]*level, n int, side Side) [][2]int64 {
	result := make([][2]int64, 0, n)
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

