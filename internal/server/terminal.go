package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// wsFrameMax bounds the size of a single WebSocket binary frame we send. The
// replay buffer for a busy TUI (Claude Code repaints its whole screen often) can
// reach hundreds of KiB; sending that as ONE frame trips the peer's read limit
// (coder/websocket defaults to 32 KiB) so the frame is silently dropped and the
// panel stays blank. Splitting into small frames keeps every frame well under any
// limit. 16 KiB is a safe, conservative size.
const wsFrameMax = 16 << 10

// wsReadLimit raises the inbound message cap so a large paste / resize burst from
// the browser is never rejected. Keystrokes and resize JSON are tiny; this is just
// headroom.
const wsReadLimit = 1 << 20

// writeChunked writes p to the connection as one or more binary frames, each at
// most wsFrameMax bytes. Returns the first write error, if any.
func writeChunked(ctx context.Context, conn *websocket.Conn, p []byte) error {
	for len(p) > 0 {
		n := len(p)
		if n > wsFrameMax {
			n = wsFrameMax
		}
		if err := conn.Write(ctx, websocket.MessageBinary, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// handleTerminal gives the browser a REAL interactive terminal by attaching to a
// live pty-backed agent (see agent.Agent). The agent tees its raw pty output to
// every subscriber; xterm.js renders that exact byte stream natively (correct
// colour, redraw, "/" menus — no snapshot tearing). Keystrokes flow back as raw
// bytes to the same pty the bridge drives, so a human can take over a session.
//
//	GET /ws/terminal?side=codex|claude
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	side := r.URL.Query().Get("side")
	if side != "codex" && side != "claude" {
		http.Error(w, "side must be codex or claude", http.StatusBadRequest)
		return
	}
	ag := s.run.Agent(side)
	if ag == nil {
		http.Error(w, "session not running; press Start first", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ch, replay, unsub := ag.Subscribe()
	defer unsub()

	// Seed the freshly-attached terminal with the current screen, chunked so a
	// large replay never exceeds the peer's frame/read limit.
	if len(replay) > 0 {
		if writeChunked(ctx, conn, replay) != nil {
			return
		}
	}

	// agent output -> websocket (also chunked: a single vt repaint can be large).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					cancel()
					return
				}
				if writeChunked(ctx, conn, chunk) != nil {
					cancel()
					return
				}
			}
		}
	}()

	// websocket -> agent stdin. Text frames starting with '{' are control
	// messages (resize); everything else is raw keystroke bytes.
	for {
		typ, data, rerr := conn.Read(ctx)
		if rerr != nil {
			return
		}
		if typ == websocket.MessageText && len(data) > 0 && data[0] == '{' {
			var m struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" {
				ag.Resize(m.Cols, m.Rows)
			}
			continue
		}
		if len(data) > 0 {
			_ = ag.Write(data)
		}
	}
}
