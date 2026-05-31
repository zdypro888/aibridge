package runner

import (
	"os/exec"
	"testing"
	"time"

	"aibridge/internal/config"
)

func TestStartPreservesPreselectedOnlySide(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	r := New()
	r.Control().OnlySide("codex")

	cfg := config.Default()
	cfg.Repo = repo
	cfg.Agents.Codex.Command = "sleep 60"
	cfg.Agents.Codex.StableFor = config.Duration(100 * time.Millisecond)
	cfg.Agents.Codex.SettleFor = config.Duration(100 * time.Millisecond)
	cfg.Agents.Codex.Timeout = config.Duration(time.Second)
	cfg.Agents.Claude.Enabled = false

	if err := r.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)

	if got := r.Control().State()["onlySide"]; got != "codex" {
		t.Fatalf("onlySide after Start = %v, want codex", got)
	}
}
