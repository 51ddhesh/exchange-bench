package orderbook

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
	ID        uint64
	Side      Side
	Type      OrderType
	Price     int64
	Qty       int64
	FilledQty int64
	ArrivedAt int64 // time.Now().UnixNano(), set by the sequencer
}

func (o *Order) RemainingQty() int64 {
	return o.Qty - o.FilledQty
}

type Fill struct {
	MakerOrderID uint64
	TakerOrderID uint64
	ExecPrice    int64
	ExecQty      int64
}

type CancelResult int8

const (
	CancelOK       CancelResult = 1
	CancelNotFound CancelResult = 2
)
