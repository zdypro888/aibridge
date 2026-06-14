package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if got := h.HandoffView(); got != (HandoffView{}) {
		t.Fatalf("late submit with no waiter must not update handoff cache, got %+v", got)
	}
}

func TestMCP_HandoffViewCachesPeerPrompt(t *testing.T) {
	h := NewMCPHub()
	codexCh := h.await("codex")
	// codex submits with a next prompt for claude.
	rpc(t, h, "codex", "tools/call", map[string]any{
		"name": "submit_review",
		"arguments": map[string]any{
			"verdict":              "FIXED",
			"next_prompt_for_peer": "verify the lock ordering",
		},
	}, 1)
	<-codexCh
	v := h.HandoffView()
	if v.Claude != "verify the lock ordering" {
		t.Fatalf("claude should have been handed the prompt, got %q", v.Claude)
	}
	if v.ClaudeConverged {
		t.Fatal("not converged (had a prompt)")
	}
	claudeCh := h.await("claude")
	// claude submits empty + no_more_bugs -> codex marked converged.
	rpc(t, h, "claude", "tools/call", map[string]any{
		"name":      "submit_review",
		"arguments": map[string]any{"verdict": "CLEAN", "no_more_bugs": true},
	}, 2)
	<-claudeCh
	v = h.HandoffView()
	if !v.CodexConverged {
		t.Fatalf("codex should be marked converged, got %+v", v)
	}
	h.Reset()
	if (h.HandoffView() != HandoffView{}) {
		t.Fatal("Reset should clear the cache")
	}
}

func TestMCP_LateSubmitAfterCancelDoesNotUpdateHandoffView(t *testing.T) {
	h := NewMCPHub()
	h.await("codex")
	h.cancelAwait("codex")

	r := rpc(t, h, "codex", "tools/call", map[string]any{
		"name": "submit_review",
		"arguments": map[string]any{
			"verdict":              "FIXED",
			"next_prompt_for_peer": "stale prompt from abandoned turn",
		},
	}, 5)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
	if got := h.HandoffView(); got != (HandoffView{}) {
		t.Fatalf("late abandoned-turn submit polluted handoff view: %+v", got)
	}
}

func TestMCPHub_ConcurrentResetDeliverAndHandoffView(t *testing.T) {
	h := NewMCPHub()
	var wg sync.WaitGroup
	for i := range 100 {
		side := "codex"
		if i%2 == 1 {
			side = "claude"
		}
		wg.Add(3)
		go func() {
			defer wg.Done()
			h.Reset()
		}()
		go func(side string) {
			defer wg.Done()
			h.await(side)
			h.deliver(side, ReviewSubmission{
				Verdict:     "CLEAN",
				NextForPeer: "prompt",
			})
		}(side)
		go func() {
			defer wg.Done()
			_ = h.HandoffView()
		}()
	}
	wg.Wait()
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
	cleanup, err := WriteMCPConfig(repo, "127.0.0.1:8799", true)
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	defer cleanup()
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
	cleanup, err := WriteMCPConfig(repo, "127.0.0.1:8799", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ts, _ := os.ReadFile(filepath.Join(repo, ".codex", "config.toml"))
	if strings.Contains(string(ts), "experimental_use_rmcp_client") {
		t.Fatalf("rmcp=false should omit the feature flag: %s", ts)
	}
	if !strings.Contains(string(ts), "/mcp/codex") {
		t.Fatalf("endpoint still required: %s", ts)
	}
}

func TestWriteMCPConfig_CleanupRestoresExistingFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	origMCP := []byte(`{"mcpServers":{"mine":{"type":"stdio"}}}`)
	origCodex := []byte("[profiles.default]\nmodel = \"x\"\n")
	origExclude := []byte("node_modules/\n")
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json"), origMCP, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml"), origCodex, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), origExclude, 0o600); err != nil {
		t.Fatal(err)
	}

	cleanup, err := WriteMCPConfig(repo, "127.0.0.1:8799", true)
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(repo, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("user-added-during-run\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for path, want := range map[string][]byte{
		filepath.Join(repo, ".mcp.json"):               origMCP,
		filepath.Join(repo, ".codex", "config.toml"):   origCodex,
		filepath.Join(repo, ".git", "info", "exclude"): append(origExclude, []byte("user-added-during-run\n")...),
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("restored %s = %q, want %q", path, got, want)
		}
	}
}

func TestWriteMCPConfig_CleanupRemovesCreatedFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanup, err := WriteMCPConfig(repo, "127.0.0.1:8799", true)
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, path := range []string{
		filepath.Join(repo, ".mcp.json"),
		filepath.Join(repo, ".codex", "config.toml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed, stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".codex")); !os.IsNotExist(err) {
		t.Fatalf(".codex dir should have been removed, stat err=%v", err)
	}
	excl, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{".mcp.json", ".mcp.json" + mcpBackupSuffix, ".codex/"} {
		if strings.Contains(string(excl), gone) {
			t.Fatalf("cleanup should remove appended exclude %q from %q", gone, excl)
		}
	}
}

func TestWriteMCPConfig_RestoresLeftoverBackupBeforeWriting(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	origMCP := []byte(`{"mcpServers":{"mine":{"type":"stdio"}}}`)
	origCodex := []byte("[profiles.default]\nmodel = \"x\"\n")
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json")+mcpBackupSuffix, origMCP, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte("generated from crashed run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml")+mcpBackupSuffix, origCodex, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml"), []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := WriteMCPConfig(repo, "127.0.0.1:8799", true)
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for path, want := range map[string][]byte{
		filepath.Join(repo, ".mcp.json"):             origMCP,
		filepath.Join(repo, ".codex", "config.toml"): origCodex,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("restored %s = %q, want %q", path, got, want)
		}
		if _, err := os.Stat(path + mcpBackupSuffix); !os.IsNotExist(err) {
			t.Fatalf("backup %s should have been consumed, stat err=%v", path+mcpBackupSuffix, err)
		}
	}
}

func TestMCPBaseURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8799": "http://127.0.0.1:8799",
		":8799":          "http://127.0.0.1:8799",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"[::]:9001":      "http://127.0.0.1:9001",
		"[::1]:9002":     "http://[::1]:9002",
	}
	for in, want := range cases {
		if got := mcpBaseURL(in); got != want {
			t.Fatalf("mcpBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
