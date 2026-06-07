import asyncio
import websockets

class Order:
    def __init__(self, oid, side, otype, price, qty, seq):
        self.oid    = oid
        self.side   = side
        self.otype  = otype
        self.price  = price
        self.qty    = qty
        self.filled = 0
        self.seq    = seq

    def remaining(self):
        return self.qty - self.filled

class Book:
    def __init__(self):
        self.bids   = []  # sorted desc price, asc seq
        self.asks   = []  # sorted asc price, asc seq
        self.orders = {}
        self.seq    = 0

    def add(self, oid, side, otype, price, qty):
        self.seq += 1
        o = Order(oid, side, otype, price, qty, self.seq)
        self.orders[oid] = o
        fills = []

        if side == 'B':
            fills = self._match(o, self.asks,
                lambda mp, tp: True if otype == 'M' else mp <= tp)
            if o.remaining() > 0 and otype == 'L':
                self._insert(self.bids, o, desc=True)
        else:
            fills = self._match(o, self.bids,
                lambda mp, tp: True if otype == 'M' else mp >= tp)
            if o.remaining() > 0 and otype == 'L':
                self._insert(self.asks, o, desc=False)

        if o.remaining() == 0:
            self.orders.pop(oid, None)
        return fills

    def _match(self, taker, makers, crosses):
        fills = []
        surviving = []
        for m in makers:
            if taker.remaining() == 0:
                surviving.append(m)
                continue
            if not crosses(m.price, taker.price):
                surviving.append(m)
                continue
            qty = min(m.remaining(), taker.remaining())
            m.filled  += qty
            taker.filled += qty
            fills.append((m.oid, taker.oid, m.price, qty))
            if m.remaining() > 0:
                surviving.append(m)
            else:
                self.orders.pop(m.oid, None)
        makers[:] = surviving
        return fills

    def cancel(self, oid):
        o = self.orders.pop(oid, None)
        if o is None:
            return False
        if o in self.bids: self.bids.remove(o)
        if o in self.asks: self.asks.remove(o)
        return True

    def _insert(self, lst, o, desc):
        i = len(lst)
        for j, x in enumerate(lst):
            if desc:
                better = o.price > x.price or (o.price == x.price and o.seq < x.seq)
            else:
                better = o.price < x.price or (o.price == x.price and o.seq < x.seq)
            if better:
                i = j
                break
        lst.insert(i, o)

def parse_price(s):
    if '.' not in s:
        return int(s) * 10000
    i, f = s.split('.', 1)
    f = (f + '0000')[:4]
    return int(i) * 10000 + int(f)

def fmt_price(p):
    return f"{p // 10000}.{p % 10000:04d}"

async def handle(ws):
    book = Book()
    async for msg in ws:
        parts = msg.split()
        if not parts:
            continue
        if parts[0] == 'ADD' and len(parts) == 6:
            oid   = parts[1]
            side  = parts[2]
            otype = parts[3]
            price = parse_price(parts[4])
            qty   = int(parts[5])
            fills = book.add(oid, side, otype, price, qty)
            for maker, taker, ep, eq in fills:
                await ws.send(f"FILL {oid} {maker} {taker} {fmt_price(ep)} {eq}")
            await ws.send(f"ACK {oid}")
        elif parts[0] == 'CAN' and len(parts) == 2:
            oid = parts[1]
            if book.cancel(oid):
                await ws.send(f"ACK {oid}")
            else:
                await ws.send(f"REJ {oid} not_found")

async def main():
    async with websockets.serve(handle, "0.0.0.0", 8080, subprotocols=[]):
        await asyncio.Future()

asyncio.run(main())