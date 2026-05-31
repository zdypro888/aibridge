package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"aibridge/internal/config"
)

func TestHandleControlRejectsInvalidSide(t *testing.T) {
	s := New(config.Default(), "")

	cases := []string{
		`{"action":"skip","side":"<img src=x onerror=alert(1)>"}`,
		`{"action":"inject","side":"other","text":"hello"}`,
		`{"action":"only","side":"other"}`,
	}

	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/control", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		s.handleControl(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("handleControl(%s) status=%d want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestHandleControlAllowsValidSides(t *testing.T) {
	s := New(config.Default(), "")

	cases := []string{
		`{"action":"skip","side":"codex"}`,
		`{"action":"inject","side":"claude","text":"hello"}`,
		`{"action":"only","side":""}`,
	}

	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/control", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		s.handleControl(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("handleControl(%s) status=%d want %d; body=%s", body, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}
