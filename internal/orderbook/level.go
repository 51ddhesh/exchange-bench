package orderbook

// single order in the doubly linked list at a price level
type node struct {
	order *Order
	prev  *node
	next  *node
}

// level holds all the resting orders at a single price (FIFO order)
type level struct {
	price int64
	head  *node // oldest - matched first
	tail  *node // newest - matched last
	total int64
}

func newLevel(price int64) *level {
	return &level{
		price: price,
	}
}

// Add a new order to the tail and returns the node as the tail
func (l *level) push(o *Order) *node {
	n := &node{
		order: o,
	}

	if l.tail == nil {
		l.head = n
		l.tail = n
	} else {
		n.prev = l.tail
		l.tail.next = n
		l.tail = n
	}

	l.total += o.RemainingQty()

	return n
}

// Pop an order after it is cancelled or filled
func (l *level) remove(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		l.head = n.next
	}

	if n.next != nil {
		n.next.prev = n.prev
	} else {
		l.tail = n.prev
	}

	n.prev = nil
	n.next = nil

	l.total -= n.order.RemainingQty()
}

// level has no resting orders
func (l *level) empty() bool {
	return l.head == nil
}
