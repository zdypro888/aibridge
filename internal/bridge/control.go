package bridge

import (
	"context"
	"fmt"
	"sync"
)

// Control is the runtime control surface the web UI drives: pause/resume the
// loop, skip the current side's turn, abort the run, restrict to a single agent,
// or inject a manual message into one side before its next turn. The loop checks
// it at turn boundaries.
type Control struct {
	mu sync.Mutex

	paused   bool
	aborted  bool
	onlySide string            // if set, only this side takes turns
	inject   map[string]string // side -> text to prepend to its next prompt
	skipNext map[string]bool   // side -> skip its next turn once

	resume chan struct{} // closed/refilled to wake a paused loop
}

// NewControl returns an idle control surface.
func NewControl() *Control {
	return &Control{
		inject:   make(map[string]string),
		skipNext: make(map[string]bool),
		resume:   make(chan struct{}, 1),
	}
}

// Pause halts the loop at the next turn boundary.
func (c *Control) Pause() {
	c.mu.Lock()
	c.paused = true
	c.mu.Unlock()
}

// Resume continues a paused loop.
func (c *Control) Resume() {
	c.mu.Lock()
	c.paused = false
	c.mu.Unlock()
	select {
	case c.resume <- struct{}{}:
	default:
	}
}

// Abort stops the run for good.
func (c *Control) Abort() {
	c.mu.Lock()
	c.aborted = true
	c.paused = false
	c.mu.Unlock()
	select {
	case c.resume <- struct{}{}:
	default:
	}
}

// OnlySide restricts the loop to a single agent ("codex"/"claude"), or "" to
// re-enable both. Lets you drive one AI manually.
func (c *Control) OnlySide(side string) {
	c.mu.Lock()
	c.onlySide = side
	c.mu.Unlock()
}

// Inject queues text to be prepended to a side's next prompt.
func (c *Control) Inject(side, text string) {
	c.mu.Lock()
	c.inject[side] = text
	c.mu.Unlock()
}

// SkipNext makes a side skip its next turn once.
func (c *Control) SkipNext(side string) {
	c.mu.Lock()
	c.skipNext[side] = true
	c.mu.Unlock()
}

// Snapshot of current control state for the UI.
func (c *Control) State() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"paused":   c.paused,
		"aborted":  c.aborted,
		"onlySide": c.onlySide,
	}
}

// gateBeforeTurn is called by the loop before each turn for the given side.
// It blocks while paused, reports abort, applies once-only skip, and returns any
// queued injection text plus whether this side is currently allowed to run.
func (c *Control) gateBeforeTurn(ctx context.Context, side string) (allowed bool, injected string, aborted error) {
	for {
		c.mu.Lock()
		if c.aborted {
			c.mu.Unlock()
			return false, "", fmt.Errorf("run aborted by user")
		}
		paused := c.paused
		c.mu.Unlock()
		if !paused {
			break
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-c.resume:
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.onlySide != "" && c.onlySide != side {
		return false, "", nil
	}
	if c.skipNext[side] {
		delete(c.skipNext, side)
		return false, "", nil
	}
	injected = c.inject[side]
	delete(c.inject, side)
	return true, injected, nil
}
