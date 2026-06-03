package bridge

import (
	"os"
	"path/filepath"
	"strings"
)

// Peer-handoff file exchange.
//
// In handoff review mode each agent, at the end of its turn, writes the prompt
// for the OTHER agent's next turn into a file under <repo>/.aibridge/. The loop
// reads that file (not the scrolling TUI screen) and feeds it back as the peer's
// next prompt — a 100% reliable channel with no length limit, no scroll loss,
// and no fragile screen parsing.
//
// An agent that believes the other side has nothing left worth reviewing writes
// the sentinel CONVERGED instead; when BOTH sides say so and the code has stopped
// changing, the run converges.

// handoffDir is the per-repo exchange directory.
const handoffDir = ".aibridge"

// HandoffConverged is the sentinel an agent writes (as the entire handoff file
// body) to signal it has nothing more for the other side to review.
const HandoffConverged = "CONVERGED"

// PrepareHandoff creates the exchange dir (ignored via .git/info/exclude) and
// clears any stale handoff files from a previous run, so the first turn of a new
// run never reads an old peer prompt. Best-effort; safe to call every run.
func PrepareHandoff(repoDir string) {
	_ = ensureHandoffDir(repoDir)
	clearHandoff(repoDir, "codex")
	clearHandoff(repoDir, "claude")
}

// handoffPath returns the file holding the prompt for side's NEXT turn.
func handoffPath(repoDir, side string) string {
	return filepath.Join(repoDir, handoffDir, "next-"+side+".md")
}

// ensureHandoffDir creates <repo>/.aibridge and makes git ignore it locally via
// .git/info/exclude (so the exchange files never pollute git diff and the user's
// own .gitignore is left untouched). Best-effort: errors are non-fatal.
func ensureHandoffDir(repoDir string) error {
	dir := filepath.Join(repoDir, handoffDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	addLocalGitExclude(repoDir, handoffDir+"/")
	return nil
}

// addLocalGitExclude appends pattern to .git/info/exclude if not already present.
// This is git's repo-local ignore list — it does not modify the tracked
// .gitignore. No-op when .git/info isn't writable (e.g. not a standard repo).
func addLocalGitExclude(repoDir, pattern string) {
	excl := filepath.Join(repoDir, ".git", "info", "exclude")
	if data, err := os.ReadFile(excl); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.TrimSpace(line) == pattern {
				return // already excluded
			}
		}
		body := string(data)
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		_ = os.WriteFile(excl, []byte(body+pattern+"\n"), 0o644)
		return
	}
	// .git/info/exclude missing: try to create it (dir may not exist).
	if err := os.MkdirAll(filepath.Join(repoDir, ".git", "info"), 0o755); err == nil {
		_ = os.WriteFile(excl, []byte(pattern+"\n"), 0o644)
	}
}

// readHandoff returns the prompt the previous agent wrote for side's next turn,
// and whether that agent declared convergence (wrote the CONVERGED sentinel).
// A missing/empty file yields ("", false): the caller falls back to the template.
func readHandoff(repoDir, side string) (prompt string, converged bool) {
	data, err := os.ReadFile(handoffPath(repoDir, side))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", false
	}
	if s == HandoffConverged {
		return "", true
	}
	return s, false
}

// clearHandoff removes side's pending handoff file so a stale prompt from a prior
// run/turn is never reused. Best-effort.
func clearHandoff(repoDir, side string) {
	_ = os.Remove(handoffPath(repoDir, side))
}

// peerConverged reports whether the agent that just finished declared the OTHER
// side has nothing left to review — i.e. it wrote CONVERGED for the peer.
func peerConverged(repoDir, peer string) bool {
	_, c := readHandoff(repoDir, peer)
	return c
}

// HandoffView is the current handoff state for the UI: the next-turn prompt each
// side has been handed, and whether either declared convergence.
type HandoffView struct {
	Codex           string `json:"codex"`           // prompt waiting for codex's next turn
	Claude          string `json:"claude"`          // prompt waiting for claude's next turn
	CodexConverged  bool   `json:"codex_converged"` // peer wrote CONVERGED for codex
	ClaudeConverged bool   `json:"claude_converged"`
}

// ReadHandoffView returns the current handoff files for display in the dashboard.
func ReadHandoffView(repoDir string) HandoffView {
	cx, cxc := readHandoff(repoDir, "codex")
	cl, clc := readHandoff(repoDir, "claude")
	return HandoffView{Codex: cx, Claude: cl, CodexConverged: cxc, ClaudeConverged: clc}
}
