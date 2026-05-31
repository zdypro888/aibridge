package bridge

import (
	"sync"
	"testing"
)

// TestBus_ConcurrentPublishUnsubscribe stresses the exact production scenario:
// the loop publishes events while SSE clients subscribe and disconnect
// (unsubscribe) concurrently. A bus that closes a subscriber channel while a
// publish may still send to it panics ("send on closed channel") — a real crash
// when a browser tab closes mid-run. Run with -race to also catch data races.
func TestBus_ConcurrentPublishUnsubscribe(t *testing.T) {
	b := NewBus(64)
	var wg sync.WaitGroup

	for range 4 { // publishers
		wg.Go(func() {
			for range 500 {
				b.Publish(Event{Kind: EventScreen, Side: "codex", Screen: "x"})
			}
		})
	}

	for range 8 { // churning subscribers
		wg.Go(func() {
			for range 200 {
				ch, _, unsub := b.Subscribe()
				select {
				case <-ch:
				default:
				}
				unsub()
			}
		})
	}

	wg.Wait()
	// Reaching here without a panic means the bus is concurrency-safe.
}
