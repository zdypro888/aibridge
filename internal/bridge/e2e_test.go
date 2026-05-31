package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aibridge/internal/agent"
	"aibridge/internal/gitx"
)

// TestEndToEnd_PtyMockAgents proves the pty drive layer works against live
// interactive processes: it spawns two mock agents on ptys, runs the full
// ping-pong loop in a temp git repo, and asserts convergence after codex fixes a
// bug and both sides go clean + confirm no-more-bugs.
//
// Requires a prebuilt mockagent binary at $MOCKAGENT. Skips otherwise.
func TestEndToEnd_PtyMockAgents(t *testing.T) {
	mock := os.Getenv("MOCKAGENT")
	if mock == "" {
		t.Skip("set MOCKAGENT to the mockagent binary to run the pty e2e test")
	}

	// Trust any dir for git children (temp dir owner can differ under sandbox).
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.directory")
	t.Setenv("GIT_CONFIG_VALUE_0", "*")

	repo := t.TempDir()
	mustGit(t, repo, "init")
	mustGit(t, repo, "config", "user.email", "t@t")
	mustGit(t, repo, "config", "user.name", "t")
	writeFile(t, filepath.Join(repo, "code.go"), "package main\n\nfunc main() {}\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-m", "init")
	// A pending change for the agents to review.
	writeFile(t, filepath.Join(repo, "code.go"), "package main\n\nfunc main() { _ = 1 }\n")

	wait := agent.WaitOpts{
		Poll:    200 * time.Millisecond,
		Stable:  3 * time.Second,
		Settle:  20 * time.Second,
		Timeout: 90 * time.Second,
	}

	fixTarget := filepath.Join(repo, "code.go")
	// codex fixes turn 1 then clean; claude clean. Both confirm NO_MORE_BUGS so
	// the default combined strategy can converge.
	codexCmd := envCmd(map[string]string{
		"MOCK_NAME": "codex", "MOCK_SCRIPT": "FIXED,CLEAN", "MOCK_FIX_FILE": fixTarget, "MOCK_NOMORE": "1",
	}, mock)
	claudeCmd := envCmd(map[string]string{
		"MOCK_NAME": "claude", "MOCK_SCRIPT": "CLEAN,CLEAN", "MOCK_NOMORE": "1",
	}, mock)

	codexAg := agent.New("codex")
	claudeAg := agent.New("claude")
	t.Cleanup(func() { codexAg.Kill(); claudeAg.Kill() })
	if err := codexAg.Start(repo, codexCmd, 120, 40); err != nil {
		t.Fatalf("start codex: %v", err)
	}
	if err := claudeAg.Start(repo, claudeCmd, 120, 40); err != nil {
		t.Fatalf("start claude: %v", err)
	}
	time.Sleep(1 * time.Second)

	cps, _ := NewPromptSet(KindDiff, "codex", "", "", "en")
	lps, _ := NewPromptSet(KindDiff, "claude", "", "", "en")
	codexDrv := NewAgentDriver("codex", codexAg, repo, wait, cps)
	claudeDrv := NewAgentDriver("claude", claudeAg, repo, wait, lps)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := Run(ctx, Config{MaxRounds: 8, FirstSide: "codex", Strategy: "combined"},
		Deps{Codex: codexDrv, Claude: claudeDrv, Hash: func() (string, error) { return gitx.Hash(repo) }})
	if err != nil {
		t.Fatalf("loop error: %v (trail=%v)", err, summarize(out))
	}
	if !out.Converged {
		t.Fatalf("expected convergence; reason=%q rounds=%d trail=%v", out.Reason, out.Rounds, summarize(out))
	}
	t.Logf("converged in %d rounds: %v", out.Rounds, summarize(out))

	if data, _ := os.ReadFile(fixTarget); !strings.Contains(string(data), "codex fix") {
		t.Fatalf("expected codex's fix in %s, got:\n%s", fixTarget, data)
	}
}

func summarize(o Outcome) []string {
	var s []string
	for _, r := range o.Trail {
		s = append(s, r.Side+":"+string(r.Verdict))
	}
	return s
}

func envCmd(env map[string]string, bin string) string {
	cmd := ""
	for k, v := range env {
		cmd += k + "='" + v + "' "
	}
	return cmd + "'" + bin + "'"
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
