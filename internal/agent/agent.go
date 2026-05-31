// Package agent runs one interactive CLI agent (claude or codex) directly under
// a pseudo-terminal owned by this process — no tmux. The pty's raw byte stream is
// teed two ways:
//
//   - into an in-memory vt10x terminal emulator, so the bridge can read a
//     pixel-accurate rendered screen for completion detection and verdict
//     parsing (Screen());
//   - to every subscribed WebSocket client, so the browser's xterm.js renders the
//     exact same byte stream natively (perfect colour, redraw, "/" menus — no
//     snapshot tearing).
//
// Input (the bridge's prompts AND the human's keystrokes) is written to the same
// pty, so a person can take over a live session and the automation resumes when
// they stop. This replaces the previous tmux-based approach, which could not
// stream output over a pty in this environment.
package agent

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// replayCap bounds the raw-output ring buffer replayed to newly-connected
// clients so they see the current screen. 512KiB easily covers an alt-screen
// TUI's recent redraws; xterm replays it in milliseconds.
const replayCap = 512 << 10

// Agent is one live pty-backed CLI agent.
type Agent struct {
	name string
	cmd  *exec.Cmd
	ptmx *os.File

	mu     sync.Mutex
	vt     vt10x.Terminal
	cols   int
	rows   int
	ring   []byte              // bounded raw-output history for client replay
	subs   map[int]chan []byte // raw-byte subscribers (websocket clients)
	nextID int
	closed bool
}

// New returns an un-started agent named "codex"/"claude".
func New(name string) *Agent {
	return &Agent{name: name, subs: make(map[int]chan []byte)}
}

func (a *Agent) Name() string { return a.name }

// Start launches `sh -c command` in dir under a pty sized cols×rows and begins
// teeing its output to the vt emulator, the replay ring, and any subscribers.
func (a *Agent) Start(dir, command string, cols, rows int) error {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	a.cmd = cmd
	a.ptmx = ptmx
	a.cols = cols
	a.rows = rows
	a.vt = vt10x.New(vt10x.WithSize(cols, rows))

	go a.readLoop()
	return nil
}

// readLoop pumps pty output into the emulator, the replay ring, and subscribers.
func (a *Agent) readLoop() {
	buf := make([]byte, 32<<10)
	for {
		n, err := a.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			a.mu.Lock()
			_, _ = a.vt.Write(chunk)
			a.ring = append(a.ring, chunk...)
			if len(a.ring) > replayCap {
				a.ring = a.ring[len(a.ring)-replayCap:]
			}
			// Fan out UNDER the lock: each send is non-blocking (drops on a slow
			// subscriber), so holding the lock costs O(subscribers) cheap sends
			// and never blocks. Doing it under the lock is what makes it safe
			// against a concurrent unsubscribe closing a channel — otherwise the
			// send would panic with "send on closed channel" (a real crash when a
			// browser tab closes mid-stream).
			for _, ch := range a.subs {
				select {
				case ch <- chunk:
				default: // slow client: drop this chunk for it
				}
			}
			a.mu.Unlock()
		}
		if err != nil {
			a.mu.Lock()
			a.closed = true
			for id, ch := range a.subs {
				close(ch)
				delete(a.subs, id)
			}
			a.mu.Unlock()
			return
		}
	}
}

// Write sends bytes to the agent's stdin (prompts from the bridge, keystrokes
// from the browser). Both share this one pty.
func (a *Agent) Write(p []byte) error {
	_, err := a.ptmx.Write(p)
	return err
}

// Screen returns the current rendered screen as plain text (from vt10x). Used by
// the bridge for completion detection and verdict parsing — pixel-accurate,
// unlike scraping raw output.
func (a *Agent) Screen() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.vt == nil {
		return ""
	}
	return a.vt.String()
}

// Alive reports whether the agent process is still running.
func (a *Agent) Alive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.closed
}

// Resize changes the pty and emulator size (browser drives this to fit its panel).
func (a *Agent) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	a.mu.Lock()
	a.cols, a.rows = cols, rows
	if a.vt != nil {
		a.vt.Resize(cols, rows)
	}
	a.mu.Unlock()
	_ = pty.Setsize(a.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Subscribe returns a channel of raw output chunks plus a replay of recent
// history (so a freshly-attached browser sees the current screen), and an
// unsubscribe func. The replay is prefixed with a full terminal reset so xterm
// starts from a clean state even if the ring begins mid-sequence.
func (a *Agent) Subscribe() (ch <-chan []byte, replay []byte, unsub func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		c := make(chan []byte)
		close(c)
		replay = make([]byte, 0, len(a.ring)+4)
		replay = append(replay, "\x1bc"...)
		replay = append(replay, a.ring...)
		return c, replay, func() {}
	}
	id := a.nextID
	a.nextID++
	c := make(chan []byte, 256)
	a.subs[id] = c

	replay = make([]byte, 0, len(a.ring)+4)
	replay = append(replay, "\x1bc"...) // RIS: full reset
	replay = append(replay, a.ring...)

	return c, replay, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if cc, ok := a.subs[id]; ok {
			delete(a.subs, id)
			close(cc)
		}
	}
}

// Kill terminates the agent process and pty.
func (a *Agent) Kill() error {
	if a.ptmx != nil {
		_ = a.ptmx.Close()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
		_, _ = a.cmd.Process.Wait()
	}
	return nil
}
