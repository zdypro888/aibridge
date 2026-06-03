package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandoff_WriteReadRoundTrip(t *testing.T) {
	repo := t.TempDir()
	PrepareHandoff(repo)

	// Simulate codex writing the next prompt for claude.
	body := "Next, dig into internal/agent: the readLoop fan-out under the mutex,\nand wait.go timeout boundaries no one verified."
	if err := os.WriteFile(handoffPath(repo, "claude"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, converged := readHandoff(repo, "claude")
	if converged {
		t.Fatal("should not be converged for a normal prompt")
	}
	if got != body {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, body)
	}
}

func TestHandoff_ConvergedSentinel(t *testing.T) {
	repo := t.TempDir()
	PrepareHandoff(repo)
	if err := os.WriteFile(handoffPath(repo, "codex"), []byte("  CONVERGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, converged := readHandoff(repo, "codex")
	if !converged {
		t.Fatal("CONVERGED sentinel not detected")
	}
	if prompt != "" {
		t.Fatalf("converged handoff should yield empty prompt, got %q", prompt)
	}
	if !peerConverged(repo, "codex") {
		t.Fatal("peerConverged should report true")
	}
}

func TestHandoff_MissingIsEmpty(t *testing.T) {
	repo := t.TempDir()
	prompt, converged := readHandoff(repo, "claude")
	if prompt != "" || converged {
		t.Fatalf("missing handoff should be empty/not-converged, got %q/%v", prompt, converged)
	}
}

func TestPrepareHandoff_ExcludesAndClears(t *testing.T) {
	repo := t.TempDir()
	// fake a git repo so .git/info/exclude path is created
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a stale handoff from a "previous run"
	os.MkdirAll(filepath.Join(repo, handoffDir), 0o755)
	os.WriteFile(handoffPath(repo, "codex"), []byte("stale"), 0o644)

	PrepareHandoff(repo)

	if _, err := os.Stat(handoffPath(repo, "codex")); !os.IsNotExist(err) {
		t.Fatal("PrepareHandoff must clear stale handoff files")
	}
	excl, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("exclude file: %v", err)
	}
	if want := handoffDir + "/"; !contains(string(excl), want) {
		t.Fatalf("exclude should contain %q, got %q", want, excl)
	}
}

func TestRenderHandoffMode_UsesPeerPromptAndEssentials(t *testing.T) {
	ps, err := NewPromptSet(KindDiff, "codex", "", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	ps.SetMode(ModeHandoff)
	peerPrompt := "FOCUS: the lock ordering in control.go pause/resume"
	out := ps.Render(peerPrompt, false)
	if !contains(out, "lock ordering in control.go") {
		t.Fatalf("handoff body (peer prompt) missing: %q", out)
	}
	// essentials must mention writing the next prompt for the peer (claude)
	if !contains(out, "next-claude.md") {
		t.Fatalf("essentials should tell codex to write next-claude.md: %q", out)
	}
	if !contains(out, "AUDIT_RESULT") {
		t.Fatalf("verdict instruction must still be forced on: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOfSub(s, sub) >= 0)
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
