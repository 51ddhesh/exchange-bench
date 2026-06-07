package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"
)

func main() {
	http.HandleFunc("/orders", handle)
	http.ListenAndServe(":8080", nil)
}

func handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	book := newBook()
	ctx := r.Context()

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		fields := strings.Fields(string(msg))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "ADD":
			if len(fields) != 6 {
				conn.Write(ctx, websocket.MessageText, []byte("REJ "+fields[1]+" bad_format"))
				continue
			}
			id := fields[1]
			side := fields[2][0]
			otype := fields[3][0]
			price := parsePrice(fields[4])
			qty, _ := strconv.ParseInt(fields[5], 10, 64)
			fills := book.add(id, side, otype, price, qty)
			for _, f := range fills {
				conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(
					"FILL %s %s %s %s %d", id, f.maker, f.taker, formatPrice(f.price), f.qty)))
			}
			conn.Write(ctx, websocket.MessageText, []byte("ACK "+id))
		case "CAN":
			if len(fields) != 2 {
				continue
			}
			if book.cancel(fields[1]) {
				conn.Write(ctx, websocket.MessageText, []byte("ACK "+fields[1]))
			} else {
				conn.Write(ctx, websocket.MessageText, []byte("REJ "+fields[1]+" not_found"))
			}
		}
	}
}

// ── matching engine ───────────────────────────────────────────────────────────

type order struct {
	id     string
	side   byte
	otype  byte
	price  int64
	qty    int64
	filled int64
	seq    int64
}

func (o *order) remaining() int64 { return o.qty - o.filled }

type fill struct {
	maker, taker string
	price, qty   int64
}

type book struct {
	bids   []*order
	asks   []*order
	orders map[string]*order
	seq    int64
}

func newBook() *book {
	return &book{orders: make(map[string]*order)}
}

func (b *book) add(id string, side, otype byte, price, qty int64) []fill {
	b.seq++
	o := &order{id: id, side: side, otype: otype, price: price, qty: qty, seq: b.seq}
	b.orders[id] = o

	var fills []fill
	if side == 'B' {
		fills = b.match(o, &b.asks, func(a, p int64) bool {
			if otype == 'M' {
				return true
			}
			return a <= p
		})
		if o.remaining() > 0 && otype == 'L' {
			b.bids = insertSorted(b.bids, o, true)
		}
	} else {
		fills = b.match(o, &b.bids, func(a, p int64) bool {
			if otype == 'M' {
				return true
			}
			return a >= p
		})
		if o.remaining() > 0 && otype == 'L' {
			b.asks = insertSorted(b.asks, o, false)
		}
	}

	if o.remaining() == 0 {
		delete(b.orders, id)
	}
	return fills
}

func (b *book) match(taker *order, makers *[]*order, crosses func(makerPrice, takerPrice int64) bool) []fill {
	var fills []fill
	surviving := (*makers)[:0]
	for _, m := range *makers {
		if taker.remaining() == 0 {
			surviving = append(surviving, m)
			continue
		}
		if !crosses(m.price, taker.price) {
			surviving = append(surviving, m)
			continue
		}
		qty := min64(m.remaining(), taker.remaining())
		m.filled += qty
		taker.filled += qty
		fills = append(fills, fill{maker: m.id, taker: taker.id, price: m.price, qty: qty})
		if m.remaining() > 0 {
			surviving = append(surviving, m)
		} else {
			delete(b.orders, m.id)
		}
	}
	*makers = surviving
	return fills
}

func (b *book) cancel(id string) bool {
	o, ok := b.orders[id]
	if !ok {
		return false
	}
	delete(b.orders, id)
	b.bids = removeOrder(b.bids, o)
	b.asks = removeOrder(b.asks, o)
	return true
}

func removeOrder(slice []*order, o *order) []*order {
	for i, x := range slice {
		if x == o {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func insertSorted(slice []*order, o *order, descPrice bool) []*order {
	i := len(slice)
	for j, x := range slice {
		better := descPrice && o.price > x.price ||
			!descPrice && o.price < x.price ||
			o.price == x.price && o.seq < x.seq
		if better {
			i = j
			break
		}
	}
	slice = append(slice, nil)
	copy(slice[i+1:], slice[i:])
	slice[i] = o
	return slice
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func parsePrice(s string) int64 {
	dot := strings.Index(s, ".")
	if dot == -1 {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n * 10000
	}
	frac := s[dot+1:]
	for len(frac) < 4 {
		frac += "0"
	}
	i, _ := strconv.ParseInt(s[:dot], 10, 64)
	f, _ := strconv.ParseInt(frac[:4], 10, 64)
	return i*10000 + f
}

func formatPrice(p int64) string {
	return fmt.Sprintf("%d.%04d", p/10000, p%10000)
}
