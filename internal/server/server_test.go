package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aibridge/internal/config"
	"aibridge/internal/promptlib"
)

func TestHandleStartRejectsMalformedJSON(t *testing.T) {
	s := New(config.Default(), "", promptlib.Default(), "")
	req := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(`{"codex":`))
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if s.run.Running() {
		t.Fatal("malformed start request must not start a run")
	}
}

func TestHandleTemplatesNormalizesBeforeStoring(t *testing.T) {
	s := New(config.Default(), "", promptlib.Default(), "")
	lib := promptlib.Library{
		Active: promptlib.DefaultTemplateID,
		Templates: []promptlib.Template{
			{ID: promptlib.DefaultTemplateID, Name: "default"},
		},
	}
	body, err := json.Marshal(lib)
	if err != nil {
		t.Fatal(err)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/templates", bytes.NewReader(body))
	postW := httptest.NewRecorder()
	s.Handler().ServeHTTP(postW, post)
	if postW.Code != http.StatusOK {
		t.Fatalf("POST /api/templates status = %d, body=%s", postW.Code, postW.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/templates", nil)
	getW := httptest.NewRecorder()
	s.Handler().ServeHTTP(getW, get)
	var got promptlib.Library
	if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Get(promptlib.FullReviewTemplateID) == nil {
		t.Fatal("stored library missing full-review built-in")
	}
	if got.Get(promptlib.ProblemTemplateID) == nil {
		t.Fatal("stored library missing problem built-in")
	}
}

func TestHandleTemplatesRejectsInvalidKind(t *testing.T) {
	s := New(config.Default(), "", promptlib.Default(), "")
	lib := promptlib.Default()
	lib.Templates = append(lib.Templates, promptlib.Template{ID: "custom", Kind: "ful"})
	body, err := json.Marshal(lib)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/templates", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestPreviewEndpointAssemblesFullPrompt guards that /api/preview returns the
// full first-turn prompt (built-in template plus the doctrine the loop appends
// each turn) for the configured mode/strategy, multi-line and with the right peer.
func TestPreviewEndpointAssemblesFullPrompt(t *testing.T) {
	cfg := config.Default()
	cfg.Flow.ReviewMode = "mcp"
	cfg.Flow.Strategy = "combined"
	cfg.Lang = "zh"
	s := New(cfg, "", promptlib.Default(), "")

	req := httptest.NewRequest(http.MethodPost, "/api/preview", strings.NewReader(`{"kind":"full","mode":"mcp"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var r struct {
		Mode, Codex, Claude string
		Ask                 bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Mode != "mcp" || !r.Ask {
		t.Fatalf("mode=%q ask=%v, want mcp/true (combined strategy asks)", r.Mode, r.Ask)
	}
	if strings.Count(r.Codex, "\n") < 10 {
		t.Fatalf("preview should be multi-line, got %d newlines", strings.Count(r.Codex, "\n"))
	}
	for _, must := range []string{"模拟运行", "避免来回改", "submit_review", "AUDIT_RESULT", "claude"} {
		if !strings.Contains(r.Codex, must) {
			t.Fatalf("codex preview missing %q", must)
		}
	}
	if !strings.Contains(r.Claude, "codex") {
		t.Fatalf("claude preview should name peer codex")
	}
}
