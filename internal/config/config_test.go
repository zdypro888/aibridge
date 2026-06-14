package config

import (
	"strings"
	"testing"
)

func TestValidateReviewMode(t *testing.T) {
	for _, mode := range []string{"", "handoff", "mcp", "rotate", "plain"} {
		t.Run("accept_"+mode, func(t *testing.T) {
			cfg := Default()
			cfg.Flow.ReviewMode = mode
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate rejected review_mode %q: %v", mode, err)
			}
		})
	}

	cfg := Default()
	cfg.Flow.ReviewMode = "mpc"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted unknown review_mode")
	}
	if !strings.Contains(err.Error(), "flow.review_mode") {
		t.Fatalf("error should name flow.review_mode, got %v", err)
	}
}
