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
