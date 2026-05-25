package workload

// Distinguish ADD and CANCEL instructions
type TickType int8

const (
	TickAdd    TickType = 1
	TickCancel TickType = 2
)

// Tick: One instruction in the deterministic workload sequence
// For TickAdd: Side, OrderType, Price ( * 10000), Quantity
// For TickCancel: OrderID is needed, others can be zero

// OrderID -> string at this layer ("o1", "o2", ...)
// Sequencer converts it to uint64 used internally by ref. orderbook
// Prices are fixed point

type Tick struct {
	Type    TickType
	OrderID string
	Side    byte  // 'B' or 'S'; 0 for TickCancel
	OrdType byte  // 'L' or 'M'; o for TickCancel
	Price   int64 // * 10000; 0 for market orders and TickCancel
	Qty     int64 // 0 for TickCancel
}
