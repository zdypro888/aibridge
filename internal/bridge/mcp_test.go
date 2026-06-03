package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rpc does a JSON-RPC POST to the hub for the given side and returns the parsed
// response.
func rpc(t *testing.T, h *MCPHub, side, method string, params any, id any) rpcResponse {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp/"+side, bytes.NewReader(b))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var resp rpcResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp
}

func TestMCP_InitializeAndToolsList(t *testing.T) {
	h := NewMCPHub()
	if r := rpc(t, h, "codex", "initialize", map[string]any{}, 1); r.Error != nil {
		t.Fatalf("initialize error: %+v", r.Error)
	}
	r := rpc(t, h, "codex", "tools/list", nil, 2)
	if r.Error != nil {
		t.Fatalf("tools/list error: %+v", r.Error)
	}
	// the submit_review tool must be advertised
	js, _ := json.Marshal(r.Result)
	if !strings.Contains(string(js), "submit_review") {
		t.Fatalf("tools/list missing submit_review: %s", js)
	}
}

func TestMCP_SubmitReviewRoutesToWaiter(t *testing.T) {
	h := NewMCPHub()
	ch := h.await("claude")

	go func() {
		rpc(t, h, "claude", "tools/call", map[string]any{
			"name": "submit_review",
			"arguments": map[string]any{
				"verdict":              "FIXED",
				"summary":              "fixed a nil deref",
				"next_prompt_for_peer": "check the lock ordering in control.go",
				"no_more_bugs":         false,
			},
		}, 3)
	}()

	sub := <-ch
	if sub.Verdict != "FIXED" {
		t.Fatalf("verdict = %q", sub.Verdict)
	}
	if sub.NextForPeer != "check the lock ordering in control.go" {
		t.Fatalf("next-for-peer = %q", sub.NextForPeer)
	}
	if sub.NoMoreBugs {
		t.Fatal("no_more_bugs should be false")
	}
}

func TestMCP_SubmitWithNoWaiterIsRecorded(t *testing.T) {
	h := NewMCPHub()
	r := rpc(t, h, "codex", "tools/call", map[string]any{
		"name":      "submit_review",
		"arguments": map[string]any{"verdict": "CLEAN", "no_more_bugs": true},
	}, 4)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
	// must not panic / must respond with content
	js, _ := json.Marshal(r.Result)
	if !strings.Contains(string(js), "recorded") {
		t.Fatalf("expected recorded ack, got %s", js)
	}
}

func TestMCP_HandoffViewCachesPeerPrompt(t *testing.T) {
	h := NewMCPHub()
	// codex submits with a next prompt for claude.
	rpc(t, h, "codex", "tools/call", map[string]any{
		"name": "submit_review",
		"arguments": map[string]any{
			"verdict":              "FIXED",
			"next_prompt_for_peer": "verify the lock ordering",
		},
	}, 1)
	v := h.HandoffView()
	if v.Claude != "verify the lock ordering" {
		t.Fatalf("claude should have been handed the prompt, got %q", v.Claude)
	}
	if v.ClaudeConverged {
		t.Fatal("not converged (had a prompt)")
	}
	// claude submits empty + no_more_bugs -> codex marked converged.
	rpc(t, h, "claude", "tools/call", map[string]any{
		"name":      "submit_review",
		"arguments": map[string]any{"verdict": "CLEAN", "no_more_bugs": true},
	}, 2)
	v = h.HandoffView()
	if !v.CodexConverged {
		t.Fatalf("codex should be marked converged, got %+v", v)
	}
	h.Reset()
	if (h.HandoffView() != HandoffView{}) {
		t.Fatal("Reset should clear the cache")
	}
}

func TestMCP_UnknownSide404(t *testing.T) {
	h := NewMCPHub()
	req := httptest.NewRequest(http.MethodPost, "/mcp/bogus", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown side, got %d", w.Code)
	}
}

func TestWriteMCPConfig_RepoScopedAndExcluded(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteMCPConfig(repo, "127.0.0.1:8799", true); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	// claude .mcp.json points at our endpoint
	mj, err := os.ReadFile(filepath.Join(repo, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mj), "/mcp/claude") {
		t.Fatalf(".mcp.json missing endpoint: %s", mj)
	}
	// codex config.toml enables rmcp + points at our endpoint
	tomlData, err := os.ReadFile(filepath.Join(repo, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ts := string(tomlData)
	if !strings.Contains(ts, "experimental_use_rmcp_client = true") || !strings.Contains(ts, "/mcp/codex") {
		t.Fatalf(".codex/config.toml wrong: %s", ts)
	}
	// both are git-excluded
	excl, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	for _, want := range []string{".mcp.json", ".codex/"} {
		if !strings.Contains(string(excl), want) {
			t.Fatalf("exclude missing %q: %s", want, excl)
		}
	}
}

func TestWriteMCPConfig_RmcpToggle(t *testing.T) {
	repo := t.TempDir()
	if err := WriteMCPConfig(repo, "127.0.0.1:8799", false); err != nil {
		t.Fatal(err)
	}
	ts, _ := os.ReadFile(filepath.Join(repo, ".codex", "config.toml"))
	if strings.Contains(string(ts), "experimental_use_rmcp_client") {
		t.Fatalf("rmcp=false should omit the feature flag: %s", ts)
	}
	if !strings.Contains(string(ts), "/mcp/codex") {
		t.Fatalf("endpoint still required: %s", ts)
	}
}

func TestMCPBaseURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8799": "http://127.0.0.1:8799",
		":8799":          "http://127.0.0.1:8799",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
	}
	for in, want := range cases {
		if got := mcpBaseURL(in); got != want {
			t.Fatalf("mcpBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
