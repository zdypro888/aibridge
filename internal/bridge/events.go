package bridge

import (
	"sync"
	"time"
)

// EventKind classifies a bridge event for UI consumers.
type EventKind string

const (
	EventRunStarted   EventKind = "run_started"
	EventTurnStarted  EventKind = "turn_started"
	EventTurnFinished EventKind = "turn_finished"
	EventScreen       EventKind = "screen" // a fresh screen capture for one side
	EventConverged    EventKind = "converged"
	EventStopped      EventKind = "stopped" // run ended (converged, capped, or aborted)
	EventLog          EventKind = "log"     // human-readable progress line
	EventControl      EventKind = "control" // a control command was applied
)

// Event is one thing that happened during a run, fanned out to all subscribers
// (the web dashboard subscribes over WebSocket). Fields are populated per kind;
// JSON-tagged for the wire.
type Event struct {
	Kind    EventKind `json:"kind"`
	Time    int64     `json:"time"` // unix millis
	Side    string    `json:"side,omitempty"`
	Round   int       `json:"round,omitempty"`
	Verdict string    `json:"verdict,omitempty"`
	Message string    `json:"message,omitempty"`
	Screen  string    `json:"screen,omitempty"`
}

// Bus is a simple fan-out event bus: the loop publishes, UI clients subscribe.
// New subscribers receive a replay of recent history so a dashboard opened
// mid-run isn't blank.
type Bus struct {
	mu      sync.Mutex
	subs    map[int]chan Event
	nextID  int
	history []Event
	maxHist int
	nowFn   func() time.Time
}

// NewBus creates an event bus retaining up to maxHistory recent events for replay.
func NewBus(maxHistory int) *Bus {
	return &Bus{
		subs:    make(map[int]chan Event),
		maxHist: maxHistory,
		nowFn:   time.Now,
	}
}

// Publish stamps and fans out an event, dropping to slow subscribers rather than
// blocking the loop. The fan-out happens UNDER the lock: each send is a
// non-blocking select (drops if the subscriber's buffer is full), so holding the
// lock costs O(subscribers) cheap sends and never blocks on a slow consumer.
// Doing it under the lock is what makes it safe against a concurrent
// unsubscribe closing a channel — otherwise Publish could send on a closed
// channel and panic (a real crash when an SSE client disconnects mid-run).
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.Time == 0 {
		e.Time = b.nowFn().UnixMilli()
	}
	b.history = append(b.history, e)
	if len(b.history) > b.maxHist {
		b.history = b.history[len(b.history)-b.maxHist:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber is slow; drop this event for it
		}
	}
}

// Subscribe returns a channel of future events plus a snapshot of recent history,
// and an unsubscribe func. The caller must call unsubscribe to avoid leaks.
func (b *Bus) Subscribe() (<-chan Event, []Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan Event, 256)
	b.subs[id] = ch
	hist := make([]Event, len(b.history))
	copy(hist, b.history)
	return ch, hist, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// Helpers for the loop to publish without constructing structs everywhere.

func (b *Bus) Log(format string, args ...any) {
	b.Publish(Event{Kind: EventLog, Message: sprintf(format, args...)})
}
