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
	"aibridge/internal/promptlib"
	"aibridge/internal/runner"
	"aibridge/internal/sessions"
)

//go:embed web
var webFS embed.FS

// Server wires the runner, config, and prompt library to HTTP handlers.
type Server struct {
	mu          sync.Mutex
	cfg         config.Config
	configPath  string
	lib         promptlib.Library // prompt-template library (persisted separately)
	promptsPath string
	run         *runner.Runner
}

// New creates a server holding the initial config + prompt library (and the
// paths to persist edits of each).
func New(cfg config.Config, configPath string, lib promptlib.Library, promptsPath string) *Server {
	return &Server{cfg: cfg, configPath: configPath, lib: lib, promptsPath: promptsPath, run: runner.New()}
}

// Handler returns the root HTTP handler (static UI + /api/*).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/templates", s.handleTemplates)
	mux.HandleFunc("/api/defaults", s.handleDefaults)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/control", s.handleControl)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/handoff", s.handleHandoff)
	mux.HandleFunc("/ws/terminal", s.handleTerminal)
	// MCP endpoint the child CLIs connect to (mcp review mode). The hub routes
	// submit_review tool calls to the driver awaiting that side's turn.
	mux.Handle("/mcp/", s.run.Hub())
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
	// Optional body chooses whether each agent resumes a prior session. An empty
	// body means "both fresh", so a bare POST still works.
	var req struct {
		Codex struct {
			Resume  bool   `json:"resume"`
			Session string `json:"session"`
		} `json:"codex"`
		Claude struct {
			Resume  bool   `json:"resume"`
			Session string `json:"session"`
		} `json:"claude"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // tolerate empty/no body

	s.mu.Lock()
	cfg := s.cfg
	tmpl := s.lib.ActiveTemplate()
	s.mu.Unlock()
	resume := runner.ResumeSet{
		Codex:  runner.Resume{Enabled: req.Codex.Resume, SessionID: req.Codex.Session},
		Claude: runner.Resume{Enabled: req.Claude.Resume, SessionID: req.Claude.Session},
	}
	if err := s.run.Start(cfg, tmpl, resume); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"status": "started"})
}

// handleTemplates serves and persists the prompt-template library.
//
//	GET  /api/templates  -> {active, templates:[...]}
//	POST /api/templates  <- the full library (replaces it, saved to disk)
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		lib := s.lib
		s.mu.Unlock()
		writeJSON(w, lib)
	case http.MethodPost:
		var lib promptlib.Library
		if err := json.NewDecoder(r.Body).Decode(&lib); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		if err := lib.Validate(); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		s.mu.Lock()
		s.lib = lib
		path := s.promptsPath
		s.mu.Unlock()
		if path != "" {
			if err := promptlib.Save(path, lib); err != nil {
				httpErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeJSON(w, map[string]string{"status": "saved"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSessions lists the resumable prior sessions for one agent in the current
// repo, most-recent first, so the dashboard can offer a "continue" picker.
//
//	GET /api/sessions?side=codex|claude
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	side := r.URL.Query().Get("side")
	if !validSide(side) {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("side must be codex or claude"))
		return
	}
	s.mu.Lock()
	repo := s.cfg.Repo
	s.mu.Unlock()
	list, err := sessions.List(side, repo)
	if err != nil {
		// Don't fail the UI over a transcript read error; return an empty list.
		list = nil
	}
	writeJSON(w, map[string]any{"sessions": list})
}

// handleHandoff returns the current peer-handoff prompts (what each side has been
// handed for its next turn) so the dashboard can show the agents steering each
// other. GET /api/handoff
func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repo := s.cfg.Repo
	mode := s.cfg.Flow.ReviewMode
	s.mu.Unlock()
	// MCP mode passes the next prompt via the submit_review tool (no .aibridge
	// files), so read it from the hub; handoff mode reads the files.
	if bridge.ReviewMode(mode) == bridge.ModeMCP {
		writeJSON(w, s.run.Hub().HandoffView())
		return
	}
	writeJSON(w, bridge.ReadHandoffView(repo))
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
	kind := r.URL.Query().Get("kind")
	cf, cn := bridge.DefaultPrompts(kind, "codex", lang)
	lf, ln := bridge.DefaultPrompts(kind, "claude", lang)
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
