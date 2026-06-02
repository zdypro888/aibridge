package agent

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"
)

// screenFn returns a goroutine-safe screen() whose value the test can change to
// simulate the TUI rendering over time.
func screenFn() (func() string, func(string)) {
	var mu sync.Mutex
	s := ""
	get := func() string { mu.Lock(); defer mu.Unlock(); return s }
	set := func(v string) { mu.Lock(); s = v; mu.Unlock() }
	return get, set
}

// TestWaitIdle_BusyDefeatsStability is the core of the "don't hand off early"
// fix: a screen that goes static while the busy marker is still present must NOT
// be treated as idle. Without the Busy guard a short Stable window would return
// almost immediately; with it, the turn only ends once the marker clears.
func TestWaitIdle_BusyDefeatsStability(t *testing.T) {
	get, set := screenFn()
	set("base")

	// Response appears and then sits visually static for a while, but the working
	// status line ("esc to interrupt") stays up — i.e. the agent is thinking.
	set("working...\n(12s · esc to interrupt)")

	opts := WaitOpts{
		Poll:    5 * time.Millisecond,
		Stable:  20 * time.Millisecond,
		Settle:  200 * time.Millisecond,
		Timeout: 2 * time.Second,
		Busy:    regexp.MustCompile(`(?i)esc to interrupt`),
	}

	done := make(chan string, 1)
	go func() {
		out, err := WaitIdle(context.Background(), opts, "base", get)
		if err != nil {
			done <- "ERR:" + err.Error()
			return
		}
		done <- out
	}()

	// Well past Stable: must still be waiting because Busy is present.
	select {
	case got := <-done:
		t.Fatalf("WaitIdle returned while still busy: %q", got)
	case <-time.After(120 * time.Millisecond):
	}

	// Busy clears and the screen settles -> now it should report idle.
	set("done.\nAUDIT_RESULT: CLEAN\n> ")
	select {
	case got := <-done:
		if got == "" || got[:4] == "ERR:" {
			t.Fatalf("expected idle screen, got %q", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("WaitIdle did not return after busy cleared")
	}
}

// TestWaitIdle_NoBusyStillWorks confirms the original stability path is intact
// when no Busy pattern is configured.
func TestWaitIdle_NoBusyStillWorks(t *testing.T) {
	get, set := screenFn()
	set("base")
	set("response text")

	opts := WaitOpts{
		Poll:    5 * time.Millisecond,
		Stable:  20 * time.Millisecond,
		Settle:  200 * time.Millisecond,
		Timeout: 2 * time.Second,
	}
	out, err := WaitIdle(context.Background(), opts, "base", get)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "response text" {
		t.Fatalf("got %q", out)
	}
}

// TestWaitIdle_BusyTimeoutBackstops ensures a turn that stays Busy forever is
// still bounded by Timeout (returns ErrTimeout, not a hang).
func TestWaitIdle_BusyTimeoutBackstops(t *testing.T) {
	get, set := screenFn()
	set("base")
	set("thinking (esc to interrupt)")

	opts := WaitOpts{
		Poll:    5 * time.Millisecond,
		Stable:  20 * time.Millisecond,
		Settle:  200 * time.Millisecond,
		Timeout: 80 * time.Millisecond,
		Busy:    regexp.MustCompile(`esc to interrupt`),
	}
	_, err := WaitIdle(context.Background(), opts, "base", get)
	if _, ok := err.(ErrTimeout); !ok {
		t.Fatalf("expected ErrTimeout when busy never clears, got %v", err)
	}
}
