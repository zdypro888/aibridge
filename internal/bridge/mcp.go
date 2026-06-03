package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// WriteMCPConfig writes the per-repo MCP client config so each CLI connects back
// to our /mcp/<side> endpoint. Repo-scoped ONLY — it never touches the user's
// global ~/.claude.json or ~/.codex/config.toml. Both written files are added to
// .git/info/exclude so they don't pollute git diff.
//   - claude: <repo>/.mcp.json (auto-loaded; trust prompt skipped under
//     --dangerously-skip-permissions)
//   - codex:  <repo>/.codex/config.toml (project-scoped; requires the rmcp
//     feature, which we enable in the same file)
func WriteMCPConfig(repoDir, addr string) error {
	base := mcpBaseURL(addr)

	// claude: .mcp.json
	claudeCfg := map[string]any{
		"mcpServers": map[string]any{
			"aibridge": map[string]any{
				"type": "http",
				"url":  base + "/mcp/claude",
			},
		},
	}
	data, err := json.MarshalIndent(claudeCfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), data, 0o644); err != nil {
		return err
	}
	addLocalGitExclude(repoDir, ".mcp.json")

	// codex: .codex/config.toml (project-scoped)
	codexDir := filepath.Join(repoDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return err
	}
	toml := "" +
		"[features]\n" +
		"experimental_use_rmcp_client = true\n\n" +
		"[mcp_servers.aibridge]\n" +
		fmt.Sprintf("url = %q\n", base+"/mcp/codex")
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(toml), 0o644); err != nil {
		return err
	}
	addLocalGitExclude(repoDir, ".codex/")
	return nil
}

// mcpBaseURL turns a listen address (possibly ":8799" or "0.0.0.0:8799") into a
// loopback base URL the child CLIs can dial.
func mcpBaseURL(addr string) string {
	host, port, found := strings.Cut(addr, ":")
	if !found { // no colon: treat whole thing as host, default port
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port
}

// MCP channel.
//
// In "mcp" review mode aibridge exposes a Model Context Protocol server over HTTP
// (JSON-RPC 2.0) at /mcp/<side>. The two real TUI agents (codex, claude) connect
// to it as MCP clients and, at the end of each turn, CALL our submit_review tool
// with structured results — instead of us scraping the verdict off the screen or
// reading a handoff file. The tool call is also the unambiguous "turn finished"
// signal.
//
// This does not change the agents being real interactive TUIs; they merely also
// connect to our MCP server.

// ReviewSubmission is the structured result an agent reports via submit_review.
type ReviewSubmission struct {
	Verdict     string // CLEAN | FIXED | ISSUES
	Summary     string // what the agent did this turn (for logs/UI)
	NextForPeer string // the next-turn prompt for the OTHER agent (may be empty)
	NoMoreBugs  bool   // agent is confident nothing remains (convergence signal)
}

// MCPHub routes submit_review calls (arriving on the HTTP MCP endpoint) to the
// driver currently awaiting that side's turn result. Safe for concurrent use.
type MCPHub struct {
	mu      sync.Mutex
	waiters map[string]chan ReviewSubmission // side -> the driver waiting for its result
}

// NewMCPHub returns an empty hub.
func NewMCPHub() *MCPHub {
	return &MCPHub{waiters: make(map[string]chan ReviewSubmission)}
}

// await registers that side's driver is waiting for a submit_review and returns
// the channel the result will arrive on. A prior pending waiter for the same side
// is replaced (its turn is over). Buffered so deliver never blocks.
func (h *MCPHub) await(side string) chan ReviewSubmission {
	ch := make(chan ReviewSubmission, 1)
	h.mu.Lock()
	h.waiters[side] = ch
	h.mu.Unlock()
	return ch
}

// cancelAwait removes side's pending waiter (turn ended via fallback/timeout) so
// a late submit_review isn't misattributed to a future turn.
func (h *MCPHub) cancelAwait(side string) {
	h.mu.Lock()
	delete(h.waiters, side)
	h.mu.Unlock()
}

// deliver hands a submission to side's waiting driver. Returns false if nobody is
// waiting (e.g. the agent called the tool when no turn expected it).
func (h *MCPHub) deliver(side string, sub ReviewSubmission) bool {
	h.mu.Lock()
	ch := h.waiters[side]
	delete(h.waiters, side)
	h.mu.Unlock()
	if ch == nil {
		return false
	}
	ch <- sub
	return true
}

// --- JSON-RPC 2.0 over HTTP (MCP streamable-http, request/response subset) ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent/null => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const mcpProtocolVersion = "2025-06-18"

// submitReviewTool is the JSON-Schema tool definition advertised to agents.
func submitReviewTool() map[string]any {
	return map[string]any{
		"name": "submit_review",
		"description": "Call this exactly once at the END of your review turn to submit your result. " +
			"verdict: CLEAN (no problems, changed nothing), FIXED (you edited code to fix real problems), or ISSUES (found problems you did not fix). " +
			"next_prompt_for_peer: the prompt telling the OTHER reviewer what to focus on next and why (leave empty and set no_more_bugs=true if nothing is left to review). " +
			"summary: a short note of what you did this turn.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict":              map[string]any{"type": "string", "enum": []string{"CLEAN", "FIXED", "ISSUES"}},
				"summary":              map[string]any{"type": "string"},
				"next_prompt_for_peer": map[string]any{"type": "string"},
				"no_more_bugs":         map[string]any{"type": "boolean"},
			},
			"required": []string{"verdict"},
		},
	}
}

// ServeHTTP implements the MCP endpoint. Mount at /mcp/ — the path's last segment
// is the side ("codex"/"claude").
func (h *MCPHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	side := strings.TrimPrefix(r.URL.Path, "/mcp/")
	side = strings.Trim(side, "/")
	if side != "codex" && side != "claude" {
		http.Error(w, "unknown side", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		// GET (server->client SSE stream) is unused; we only handle request/response.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}

	// Notifications (no id) get a 202 with no body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "aibridge", "version": "1"},
		})
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": []any{submitReviewTool()}})
	case "tools/call":
		h.handleToolCall(w, side, req)
	default:
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (h *MCPHub) handleToolCall(w http.ResponseWriter, side string, req rpcRequest) {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Verdict     string `json:"verdict"`
			Summary     string `json:"summary"`
			NextForPeer string `json:"next_prompt_for_peer"`
			NoMoreBugs  bool   `json:"no_more_bugs"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	if p.Name != "submit_review" {
		writeRPCError(w, req.ID, -32601, "unknown tool: "+p.Name)
		return
	}
	sub := ReviewSubmission{
		Verdict:     strings.ToUpper(strings.TrimSpace(p.Arguments.Verdict)),
		Summary:     p.Arguments.Summary,
		NextForPeer: strings.TrimSpace(p.Arguments.NextForPeer),
		NoMoreBugs:  p.Arguments.NoMoreBugs,
	}
	delivered := h.deliver(side, sub)
	msg := "review recorded"
	if !delivered {
		msg = "review recorded (no turn was awaiting it)"
	}
	writeRPCResult(w, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
	})
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	if id == nil {
		id = json.RawMessage("null")
	}
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}
