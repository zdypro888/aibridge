package agent

import (
	"strings"
	"testing"
	"time"
)

// TestAgent_StreamScreenInput verifies the real pty data path: an agent started
// on a pty (1) renders its output into the vt10x screen, (2) streams raw output
// to subscribers, and (3) delivers bytes written to it back to the process —
// proving the browser terminal's read+write paths and the bridge's Screen() read.
func TestAgent_StreamScreenInput(t *testing.T) {
	// A shell that prints a marker, then echoes a line it reads.
	a := New("test")
	t.Cleanup(func() { a.Kill() })
	sh := `printf 'MARKER_READY\n'; read x; printf "GOT:%s\n" "$x"; sleep 3`
	if err := a.Start(t.TempDir(), "sh -c "+shquote(sh), 80, 24); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Subscribe like a websocket client; collect streamed output.
	ch, replay, unsub := a.Subscribe()
	defer unsub()
	got := string(replay)

	// 1) streamed output reaches the subscriber.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(got, "MARKER_READY") {
		select {
		case b := <-ch:
			got += string(b)
		case <-deadline:
			t.Fatalf("subscriber never received MARKER_READY; got %q", got)
		}
	}

	// 2) Screen() (vt10x render) shows it too.
	if scr := a.Screen(); !strings.Contains(scr, "MARKER_READY") {
		t.Fatalf("Screen() missing MARKER_READY: %q", scr)
	}

	// 3) input written to the agent reaches the process (echoed as GOT:hello).
	if err := a.Write([]byte("hello\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline = time.After(5 * time.Second)
	for !strings.Contains(got, "GOT:hello") {
		select {
		case b := <-ch:
			got += string(b)
		case <-deadline:
			t.Fatalf("input did not reach process; got %q", got)
		}
	}
}

func TestAgent_SubscribeAfterExitClosesChannel(t *testing.T) {
	a := New("test")
	if err := a.Start(t.TempDir(), "printf done", 80, 24); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { a.Kill() })

	deadline := time.After(5 * time.Second)
	for a.Alive() {
		select {
		case <-deadline:
			t.Fatal("agent did not exit")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	ch, _, unsub := a.Subscribe()
	defer unsub()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed subscription channel")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription after exit did not close")
	}
}

func shquote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
