package orderbook

import (
	"testing"
)

func newOrder(id string, side Side, _type OrderType, price, qty int64) Order {
	return Order{
		ID:    id,
		Side:  side,
		Type:  _type,
		Price: price,
		Qty:   qty,
	}
}

func TestLimitBuyMatchesSell(t *testing.T) {
	b := NewBook()

	b.Add(newOrder("o1", Buy, Limit, 1005000, 10)) // rests
	fills, rests := b.Add(newOrder("o2", Sell, Limit, 1005000, 10))

	if rests {
		t.Fatal("fully matched order should not rest")
	}
	if len(fills) != 1 {
		t.Fatalf("want 1 fill, got %d", len(fills))
	}
	f := fills[0]
	if f.MakerOrderID != "o1" || f.TakerOrderID != "o2" {
		t.Errorf("wrong maker/taker: %+v", f)
	}
	if f.ExecQty != 10 {
		t.Errorf("want qty 10, got %d", f.ExecQty)
	}
	if f.ExecPrice != 1005000 {
		t.Errorf("want price 1005000, got %d", f.ExecPrice)
	}
}
