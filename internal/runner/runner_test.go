package runner

import (
	"testing"

	"aibridge/internal/config"
	"aibridge/internal/promptlib"
)

// TestStart_RejectsNonRepo ensures Start fails fast (and synchronously) when the
// configured repo is not a git work tree, instead of leaking a half-launched run.
func TestStart_RejectsNonRepo(t *testing.T) {
	r := New()
	cfg := config.Default()
	cfg.Repo = t.TempDir() // a dir but not a git repo
	cfg.Server.Addr = "127.0.0.1:0"

	lib := promptlib.Default()
	if err := r.Start(cfg, lib.ActiveTemplate()); err == nil {
		t.Fatal("expected Start to reject a non-repo work tree")
	}

	if r.Running() {
		t.Fatal("runner should not be marked running after a failed Start")
	}
}
