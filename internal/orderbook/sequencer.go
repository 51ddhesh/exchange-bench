package orderbook

import (
	"sync"
	"sync/atomic"
	"time"
)

// Sequencer is the single boundary between the network layer and the engine.
//
// Responsibilities:
//  1. Assign stable uint64 internal IDs to incoming string order IDs.
//  2. Stamp ArrivedAt with time.Now().UnixNano() — the only place in the
//     matching path where wall time is read.
//  3. Forward orders to the Engine via Submit/CancelOrder.
//
// Multiple network goroutines may call Add and Cancel concurrently.
// The ID maps are protected by a RWMutex. ID assignment uses atomic increment
// so the critical section for map writes is as short as possible.
type Sequencer struct {
	engine *Engine

	mu     sync.RWMutex
	idMap  map[string]uint64 // external string ID → internal uint64
	revMap map[uint64]string // internal uint64 → external string ID

	nextID uint64 // atomically incremented
}

func NewSequencer(engine *Engine) *Sequencer {
	return &Sequencer{
		engine: engine,
		idMap:  make(map[string]uint64),
		revMap: make(map[uint64]string),
		nextID: 1,
	}
}

// Add assigns an internal ID, stamps ArrivedAt, and submits to the engine.
// Returns fills, whether the order rests, and the internal ID assigned.
func (s *Sequencer) Add(extID string, side Side, typ OrderType, price, qty int64) ([]Fill, bool, uint64) {
	// Atomically claim the next ID, then record the mapping under the write lock.
	id := atomic.AddUint64(&s.nextID, 1) - 1

	s.mu.Lock()
	s.idMap[extID] = id
	s.revMap[id] = extID
	s.mu.Unlock()

	// Stamp time here — never inside the matching engine.
	o := Order{
		ID:        id,
		Side:      side,
		Type:      typ,
		Price:     price,
		Qty:       qty,
		ArrivedAt: time.Now().UnixNano(),
	}

	fills, rests := s.engine.Submit(o)
	return fills, rests, id
}

// Cancel looks up the internal ID for extID and forwards to the engine.
func (s *Sequencer) Cancel(extID string) CancelResult {
	s.mu.RLock()
	id, ok := s.idMap[extID]
	s.mu.RUnlock()

	if !ok {
		return CancelNotFound
	}
	return s.engine.CancelOrder(id)
}

// ExternalID returns the protocol string ID for a given internal uint64.
// Used when building FILL response lines from engine output.
func (s *Sequencer) ExternalID(id uint64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revMap[id]
}

// Reset clears all ID mappings, resets the counter, and resets the engine.
// Call this between runs. The engine's Run goroutine must be stopped first.
func (s *Sequencer) Reset() {
	s.mu.Lock()
	s.idMap = make(map[string]uint64)
	s.revMap = make(map[uint64]string)
	atomic.StoreUint64(&s.nextID, 1)
	s.mu.Unlock()

	s.engine.Reset()
}
