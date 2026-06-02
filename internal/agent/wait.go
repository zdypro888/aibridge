package agent

import (
	"context"
	"regexp"
	"time"
)

// WaitOpts tunes how completion is detected for an interactive TUI turn.
type WaitOpts struct {
	Poll    time.Duration // how often to sample the screen
	Stable  time.Duration // screen must be unchanged this long (after it first moves) to count as idle
	Settle  time.Duration // max time to wait for the screen to FIRST change after submit before giving up on "it moved"
	Timeout time.Duration // overall ceiling; returns ErrTimeout
	// Busy, when non-nil, matches the agent TUI's "working" status line (e.g.
	// "esc to interrupt"). While the rendered screen matches Busy, the turn is
	// treated as still in progress no matter how long the screen has otherwise
	// been visually unchanged — defeating the false "idle" a thinking/streaming
	// agent triggers when it pauses (deep thinking, a slow tool/API call) with a
	// static screen. Only Timeout can end a turn that stays Busy forever.
	Busy *regexp.Regexp
}

// DefaultWaitOpts is a conservative baseline: poll twice a second, require 4s of
// quiet after the response starts streaming, wait up to 30s for the response to
// begin, and cap a turn at 10 minutes.
func DefaultWaitOpts() WaitOpts {
	return WaitOpts{
		Poll:    500 * time.Millisecond,
		Stable:  4 * time.Second,
		Settle:  30 * time.Second,
		Timeout: 10 * time.Minute,
	}
}

// ErrTimeout is returned when WaitIdle exceeds its Timeout.
type ErrTimeout struct{}

func (ErrTimeout) Error() string { return "agent.WaitIdle: timeout waiting for turn to finish" }

// WaitIdle blocks until the agent's turn appears finished, sampling the rendered
// screen via screen(). Completion = the screen first DIVERGES from baseline (the
// response started rendering), then stays unchanged for Stable. Requiring
// divergence is essential: a slow-to-start agent would otherwise let WaitIdle
// return the stale pre-submit screen and the loop would misread the turn.
//
// baseline is the screen captured immediately BEFORE the prompt was submitted.
// The returned string is the final sampled screen.
func WaitIdle(ctx context.Context, o WaitOpts, baseline string, screen func() string) (string, error) {
	start := time.Now()
	ticker := time.NewTicker(o.Poll)
	defer ticker.Stop()

	moved := false
	var last string
	var lastChange time.Time
	settleDeadline := start.Add(o.Settle)

	for {
		select {
		case <-ctx.Done():
			return screen(), ctx.Err()
		case <-ticker.C:
		}

		if time.Since(start) > o.Timeout {
			return screen(), ErrTimeout{}
		}

		cur := screen()

		// A visible "working" status line means the turn is still in progress even
		// if the screen is momentarily static (deep thinking, a slow tool/API
		// call). Treat Busy like the start of activity: mark moved, hold the
		// stability clock open, and never return idle this tick.
		busy := o.Busy != nil && o.Busy.MatchString(cur)

		if !moved {
			if cur != baseline || busy {
				moved = true
				last = cur
				lastChange = time.Now()
			} else if time.Now().After(settleDeadline) {
				// Nothing ever appeared; caller treats an unparseable screen as
				// UNKNOWN (never CLEAN), so this can't cause a false convergence.
				return cur, ErrTimeout{}
			}
			continue
		}

		if busy {
			// Still working: keep the stability window from ever elapsing.
			last = cur
			lastChange = time.Now()
			continue
		}

		if cur != last {
			last = cur
			lastChange = time.Now()
			continue
		}
		if time.Since(lastChange) >= o.Stable {
			return cur, nil
		}
	}
}
