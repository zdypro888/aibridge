package runner

import (
	"testing"

	"aibridge/internal/config"
	"aibridge/internal/promptlib"
)

// TestStart_RejectsNonRepo verifies Start fails fast when the configured repo
// isn't a git work tree (before launching any agent).
func TestStart_RejectsNonRepo(t *testing.T) {
	r := New()
	cfg := config.Default()
	cfg.Repo = t.TempDir() // a real dir, but not a git repo
	lib := promptlib.Default()
	if err := r.Start(cfg, lib.ActiveTemplate(), ResumeSet{}, ""); err == nil {
		t.Fatal("expected Start to reject a non-repo directory")
	}
	if r.Running() {
		t.Fatalf("runner should not be running after a rejected start")
	}
}

func TestApplyResume(t *testing.T) {
	cases := []struct {
		name    string
		side    string
		command string
		resume  Resume
		want    string
	}{
		{"disabled passthrough", "claude", "claude --x", Resume{Enabled: false}, "claude --x"},
		{"claude continue", "claude", "claude --x", Resume{Enabled: true}, "claude --x --continue"},
		{"claude resume id", "claude", "claude --x", Resume{Enabled: true, SessionID: "abc-123"}, "claude --x --resume 'abc-123'"},
		{"codex last, bare binary", "codex", "/p/codex", Resume{Enabled: true}, "/p/codex resume --last"},
		{"codex resume id", "codex", "/p/codex", Resume{Enabled: true, SessionID: "u1"}, "/p/codex resume 'u1'"},
		{"codex keeps trailing args", "codex", "/p/codex --foo", Resume{Enabled: true}, "/p/codex resume --last --foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyResume(c.side, c.command, c.resume); got != c.want {
				t.Fatalf("applyResume(%q,%q,%+v) = %q, want %q", c.side, c.command, c.resume, got, c.want)
			}
		})
	}
}
