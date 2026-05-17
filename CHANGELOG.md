# Changelog

All notable changes to this project are documented here. 

## May 16, 2026

### Added 
- [types](./internal/orderbook/types.go): Contains the generic types used. 
- [orderbook](./internal/orderbook/book.go): Reference price-time priority orderbook.
- [price level](./internal/orderbook/level.go): all resting orders at a single price level in FIFO order. 
- [matching engine](./internal/orderbook/engine.go): matching engine - matches the buy and sell orders.