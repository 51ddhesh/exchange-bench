use std::net::TcpListener;
use std::net::TcpStream;
use std::thread;
use tungstenite::accept;
use tungstenite::Message;
use tungstenite::WebSocket;

fn main() {
    let listener = TcpListener::bind("0.0.0.0:8080").unwrap();
    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                s.set_nodelay(true).ok();
                thread::spawn(move || handle(s));
            }
            Err(_) => {}
        }
    }
}

fn handle(stream: TcpStream) {
    let mut ws: WebSocket<TcpStream> = match accept(stream) {
        Ok(w)  => w,
        Err(_) => return,
    };

    let mut book = Book::new();

    loop {
        let msg = match ws.read() {
            Ok(m)  => m,
            Err(_) => return,
        };
        let text = match msg {
            Message::Text(t)  => t,
            Message::Close(_) => {
                ws.close(None).ok();
                return;
            }
            Message::Ping(d) => {
                ws.send(Message::Pong(d)).ok();
                continue;
            }
            _ => continue,
        };
        let parts: Vec<&str> = text.split_whitespace().collect();
        if parts.is_empty() { continue; }

        match parts[0] {
            "ADD" if parts.len() == 6 => {
                let id    = parts[1];
                let side  = parts[2].as_bytes()[0];
                let otype = parts[3].as_bytes()[0];
                let price = parse_price(parts[4]);
                let qty: i64 = parts[5].parse().unwrap_or(0);
                let fills = book.add(id, side, otype, price, qty);
                for f in &fills {
                    let line = format!("FILL {} {} {} {} {}",
                        id, f.maker, f.taker, fmt_price(f.price), f.qty);
                    if ws.send(Message::Text(line)).is_err() { return; }
                }
                if ws.send(Message::Text(format!("ACK {}", id))).is_err() { return; }
            }
            "CAN" if parts.len() == 2 => {
                let id = parts[1];
                if book.cancel(id) {
                    if ws.send(Message::Text(format!("ACK {}", id))).is_err() { return; }
                } else {
                    if ws.send(Message::Text(format!("REJ {} not_found", id))).is_err() { return; }
                }
            }
            _ => {}
        }
    }
}

#[derive(Clone)]
struct Order {
    id:     String,
    otype:  u8,
    price:  i64,
    qty:    i64,
    filled: i64,
    seq:    i64,
}

impl Order {
    fn remaining(&self) -> i64 { self.qty - self.filled }
}

struct Fill {
    maker: String,
    taker: String,
    price: i64,
    qty:   i64,
}

struct Book {
    bids: Vec<Order>,
    asks: Vec<Order>,
    seq:  i64,
}

impl Book {
    fn new() -> Self { Book { bids: vec![], asks: vec![], seq: 0 } }

    fn add(&mut self, id: &str, side: u8, otype: u8, price: i64, qty: i64) -> Vec<Fill> {
        self.seq += 1;
        let mut taker = Order {
            id: id.to_string(), otype, price, qty, filled: 0, seq: self.seq,
        };
        let fills;

        if side == b'B' {
            fills = Self::match_orders(&mut taker, &mut self.asks,
                |mp, tp, ot| if ot == b'M' { true } else { mp <= tp });
            if taker.remaining() > 0 && otype == b'L' {
                Self::insert_sorted(&mut self.bids, taker, true);
            }
        } else {
            fills = Self::match_orders(&mut taker, &mut self.bids,
                |mp, tp, ot| if ot == b'M' { true } else { mp >= tp });
            if taker.remaining() > 0 && otype == b'L' {
                Self::insert_sorted(&mut self.asks, taker, false);
            }
        }
        fills
    }

    fn match_orders(
        taker: &mut Order,
        makers: &mut Vec<Order>,
        crosses: impl Fn(i64, i64, u8) -> bool,
    ) -> Vec<Fill> {
        let mut fills = vec![];
        let mut surviving = vec![];
        for mut m in makers.drain(..) {
            if taker.remaining() == 0 { surviving.push(m); continue; }
            if !crosses(m.price, taker.price, taker.otype) { surviving.push(m); continue; }
            let qty = taker.remaining().min(m.remaining());
            m.filled     += qty;
            taker.filled += qty;
            fills.push(Fill { maker: m.id.clone(), taker: taker.id.clone(), price: m.price, qty });
            if m.remaining() > 0 { surviving.push(m); }
        }
        *makers = surviving;
        fills
    }

    fn cancel(&mut self, id: &str) -> bool {
        if let Some(i) = self.bids.iter().position(|o| o.id == id) {
            self.bids.remove(i); return true;
        }
        if let Some(i) = self.asks.iter().position(|o| o.id == id) {
            self.asks.remove(i); return true;
        }
        false
    }

    fn insert_sorted(lst: &mut Vec<Order>, o: Order, desc: bool) {
        let pos = lst.iter().position(|x| {
            if desc { o.price > x.price || (o.price == x.price && o.seq < x.seq) }
            else    { o.price < x.price || (o.price == x.price && o.seq < x.seq) }
        }).unwrap_or(lst.len());
        lst.insert(pos, o);
    }
}

fn parse_price(s: &str) -> i64 {
    match s.find('.') {
        None => s.parse::<i64>().unwrap_or(0) * 10000,
        Some(d) => {
            let int_part:  i64 = s[..d].parse().unwrap_or(0);
            let frac_str       = format!("{:0<4}", &s[d+1..]);
            let frac_part: i64 = frac_str[..4].parse().unwrap_or(0);
            int_part * 10000 + frac_part
        }
    }
}

fn fmt_price(p: i64) -> String {
    format!("{}.{:04}", p / 10000, p % 10000)
}
