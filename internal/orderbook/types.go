package orderbook

import (
	"time"
)

type Side int8

const (
	Buy  Side = 1
	Sell Side = -1
)

type OrderType int8

const (
	Limit  OrderType = 1
	Market OrderType = 2
)

type Order struct {
	ID        string
	Side      Side
	Type      OrderType
	Price     int64
	Qty       int64
	FilledQty int64
	ArrivedAt time.Time
}

func (o *Order) RemainingQty() int64 {
	return o.Qty - o.FilledQty
}

type Fill struct {
	MakerOrderID string
	TakerOrderID string
	ExecPrice    int64
	ExecQty      int64
}

type CancelResult int8

const (
	CancelOK       CancelResult = 1
	CancelNotFound CancelResult = 2
)
