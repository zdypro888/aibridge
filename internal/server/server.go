// Package server exposes the bridge as a web dashboard: a REST API to edit
// config and steer the run, an event stream for turn verdicts and convergence,
// WebSocket-backed live terminals, and an embedded single-page UI.
package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"

	"aibridge/internal/bridge"
	"aibridge/internal/config"
	"aibridge/internal/runner"
)

//go:embed web
var webFS embed.FS

// Server wires the runner and config to HTTP handlers.
type Server struct {
	mu         sync.Mutex
	cfg        config.Config
	configPath string
	run        *runner.Runner
}

// New creates a server holding the initial config (and the path to persist edits).
func New(cfg config.Config, configPath string) *Server {
	return &Server{cfg: cfg, configPath: configPath, run: runner.New()}
}

// Handler returns the root HTTP handler (static UI + /api/*).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/defaults", s.handleDefaults)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/control", s.handleControl)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/ws/terminal", s.handleTerminal)
	return mux
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		cfg := s.cfg
		s.mu.Unlock()
		writeJSON(w, cfg)
	case http.MethodPost:
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		if err := cfg.Validate(); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		s.mu.Lock()
		s.cfg = cfg
		path := s.configPath
		s.mu.Unlock()
		if path != "" {
			if err := config.Save(path, cfg); err != nil {
				httpErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeJSON(w, map[string]string{"status": "saved"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if err := s.run.Start(cfg); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"status": "started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.run.Stop()
	writeJSON(w, map[string]string{"status": "stopping"})
}

// handleControl applies a runtime steering command.
//
//	{"action":"pause"}                       pause the loop
//	{"action":"resume"}                      resume
//	{"action":"skip","side":"codex"}         skip a side's next turn
//	{"action":"only","side":"claude"}        restrict to one side ("" = both)
//	{"action":"inject","side":"codex","text":"..."}  prepend text to next prompt
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string `json:"action"`
		Side   string `json:"side"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	ctrl := s.run.Control()
	switch req.Action {
	case "pause":
		ctrl.Pause()
	case "resume":
		ctrl.Resume()
	case "skip":
		if !validSide(req.Side) {
			httpErr(w, http.StatusBadRequest, fmt.Errorf("side must be codex or claude"))
			return
		}
		ctrl.SkipNext(req.Side)
	case "only":
		if req.Side != "" && !validSide(req.Side) {
			httpErr(w, http.StatusBadRequest, fmt.Errorf("side must be codex, claude, or empty"))
			return
		}
		ctrl.OnlySide(req.Side)
	case "inject":
		if !validSide(req.Side) {
			httpErr(w, http.StatusBadRequest, fmt.Errorf("side must be codex or claude"))
			return
		}
		ctrl.Inject(req.Side, req.Text)
	default:
		httpErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", req.Action))
		return
	}
	s.run.Bus().Publish(bridge.Event{Kind: bridge.EventControl, Side: req.Side, Message: req.Action})
	writeJSON(w, map[string]string{"status": "ok"})
}

func validSide(side string) bool {
	return side == "codex" || side == "claude"
}

// handleDefaults returns the built-in prompt templates for the given language so
// the UI can show them as placeholders (an empty per-agent prompt = use these).
//
//	GET /api/defaults?lang=zh|en
func (s *Server) handleDefaults(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		s.mu.Lock()
		lang = s.cfg.Lang
		s.mu.Unlock()
	}
	cf, cn := bridge.DefaultPrompts("codex", lang)
	lf, ln := bridge.DefaultPrompts("claude", lang)
	writeJSON(w, map[string]any{
		"codex":  map[string]string{"first": cf, "next": cn},
		"claude": map[string]string{"first": lf, "next": ln},
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{
		"running": s.run.Running(),
		"control": s.run.Control().State(),
	}
	if out := s.run.LastOutcome(); out != nil {
		status["last"] = out
	}
	writeJSON(w, status)
}

// handleEvents streams bus events as SSE until the client disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, history, unsub := s.run.Bus().Subscribe()
	defer unsub()

	// Replay recent history so a freshly-opened dashboard isn't blank.
	for _, e := range history {
		writeSSE(w, e)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, e)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e bridge.Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
