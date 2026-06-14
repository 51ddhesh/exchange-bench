# ExchangeBench Competitor Guide

Welcome to ExchangeBench! This guide defines the rules, technical requirements, and communication protocols you must follow to build a high-performance matching engine for the competition.

---

## 1. Environment & WebSocket Requirements

Your matching engine will run inside a tightly constrained sandbox. You must adhere to the following network and environment rules:

### WebSocket Server
- **Host & Port:** Your application must start a WebSocket server listening on `0.0.0.0:8080`.
- **Endpoint:** The server must accept connections on the `/orders` route.
- **Concurrency:** The benchmarking platform will open **multiple concurrent WebSocket connections** to your server.
- **State Isolation:** ⚠️ **CRITICAL:** You must instantiate a completely independent, isolated matching engine for **each** WebSocket connection. Do not share order books across different connections. The platform evaluates bots concurrently; sharing state will cause your engine to generate false fills and fail the correctness checks.

### Sandbox Limits
- **CPU:** 2 Cores
- **Memory:** 512 MB (No swap space)
- **Binary Size:** Must be ≤ 50 MB
- **Network:** Isolated. You have zero outbound internet access.
- **Filesystem:** Read-only root. You have access to a 64MB `tmpfs` at `/tmp`.

---

## 2. Interaction Protocol

Communication happens strictly via ASCII text over WebSocket frames. You will receive commands from the platform and must reply with appropriate responses.

### Inbound Commands (Platform → You)
Each incoming WebSocket frame contains exactly one command.

1. **Add Order (`ADD`)**
   Format: `ADD <order_id> <side> <type> <price> <qty>`
   - `order_id`: Alphanumeric string.
   - `side`: `B` (Buy) or `S` (Sell).
   - `type`: `L` (Limit) or `M` (Market).
   - `price`: String float (e.g., `100.5000`).
   - `qty`: Integer.
   - *Example:* `ADD o1 B L 100.5000 10`

2. **Cancel Order (`CAN`)**
   Format: `CAN <order_id>`
   - *Example:* `CAN o1`

### Outbound Responses (You → Platform)
You must reply to every command. Each response must be sent as its own individual WebSocket text frame.

1. **Acknowledge (`ACK`)**
   Format: `ACK <order_id>`
   - Signals that a command was successfully processed.

2. **Reject (`REJ`)**
   Format: `REJ <order_id> <reason>`
   - Signals that a command failed. Reason is a custom string (e.g., `not_found`).

3. **Fill (`FILL`)**
   Format: `FILL <taker_order_id> <maker_order_id> <exec_price> <exec_qty>`
   - Signals that a trade occurred.
   - `taker_order_id`: The ID of the aggressive order that crossed the spread.
   - `maker_order_id`: The ID of the resting order in the book.
   - `exec_price`: The price of the resting maker order.
   - `exec_qty`: The amount traded.

---

## 3. How to Decide: ACK, REJ, and FILL

Correctness is heavily penalized if done wrong. The platform's validator checks your output against a perfect reference engine. Here is the exact logic your engine must use to decide how to respond:

### Processing an `ADD` Command
When you receive an `ADD` command, you should **almost always end with an `ACK`**.

1. **Did it cross the spread?** 
   If the incoming order matches against resting orders, you must emit one `FILL` frame for **every** matched resting order. You must emit all `FILL` frames **before** sending the terminal `ACK`.
2. **Did it rest in the book?**
   If it didn't match (or partially matched and the remainder rests), simply add it to your book and send an `ACK`.
3. **When to `REJ` an `ADD`?**
   You should only `REJ` an `ADD` order if the incoming string is physically malformed (e.g., missing fields, unparseable quantities). Since the platform generates clean workloads, you will likely never need to `REJ` an `ADD` during the benchmark.

*Example Flow of an `ADD` that trades:*
```text
Platform:   ADD t1 B L 100.50 2
Contestant: FILL t1 m1 100.50 2    <-- Sent first
Contestant: ACK t1                 <-- Sent last
```

### Processing a `CAN` Command
When you receive a `CAN` command, your response depends on the state of your resting order book.

1. **Send `ACK`:**
   If the `order_id` exists in your resting book, remove it and send an `ACK`.
2. **Send `REJ`:**
   If the `order_id` is **NOT** found in your resting book, send a `REJ`. 
   *Why wouldn't it be found?* 
   - It was previously fully filled.
   - It was already cancelled.
   - The ID never existed.

*Example Flow of a `CAN`:*
```text
Platform:   CAN m1
Contestant: ACK m1    <-- If m1 was successfully removed

Platform:   CAN fake1
Contestant: REJ fake1 not_found   <-- If fake1 wasn't in the book
```

---

## 4. General Guidelines & Pitfalls

- **No Zombie Fills:** If you successfully `ACK` a cancel request for an order, you must never emit a `FILL` for that order later. Doing so is a "Zombie Fill" and will immediately flag your submission as failed.
- **Price-Time Priority:** You must maintain strict FIFO (First-In, First-Out) priority at each price level. If two orders are resting at $100, the one that arrived first must be filled first.
- **No Floating Point Math:** Avoid using `float64` or `double` for prices, as floating-point inaccuracies can cause incorrect matching. Parse prices by stripping the decimal (e.g., `"100.5000"` → `1005000`) and use integer arithmetic.
- **Library Selection:** Choose your WebSocket library carefully. The benchmark measures how fast your WebSocket server can parse frames and route them to your engine. 
  - *C++:* `uWebSockets`
  - *Rust:* `tungstenite`
  - *Go:* `coder/websocket`
