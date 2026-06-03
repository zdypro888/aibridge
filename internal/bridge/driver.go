package bridge

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"aibridge/internal/agent"
	"aibridge/internal/gitx"
)

// verdictRe matches the verdict line, anchored to a line start so prose that
// merely mentions the token doesn't match. We take the LAST match because the
// submitted prompt (echoed on screen) can also contain the token.
var verdictRe = regexp.MustCompile(`(?im)^\s*AUDIT_RESULT:\s*(CLEAN|FIXED|ISSUES)\b`)

var (
	noMoreBugsRe = regexp.MustCompile(`(?i)\bNO_MORE_BUGS\b`)
	moreBugsRe   = regexp.MustCompile(`(?i)\bMORE_BUGS\b`)
)

// submitEnterDelay is the pause between writing a prompt and sending Enter, so
// the TUI's paste-burst debounce flushes and treats the \r as a real keypress
// (otherwise the long prompt is buffered as a paste and never submitted).
const submitEnterDelay = 250 * time.Millisecond

// debugf writes to stderr when AIBRIDGE_DEBUG is set.
func debugf(format string, a ...any) {
	if os.Getenv("AIBRIDGE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[aibridge] "+format+"\n", a...)
	}
}

// dumpScreen writes the screen the bridge parsed to /tmp/aibridge-<side>-screen.txt
// when AIBRIDGE_DEBUG is set, so a turn's exact parsed content can be inspected.
func dumpScreen(side, screen string) {
	if os.Getenv("AIBRIDGE_DEBUG") == "" {
		return
	}
	_ = os.WriteFile("/tmp/aibridge-"+side+"-screen.txt", []byte(screen), 0o644)
}

// AgentDriver drives a real interactive agent running on a pty (see agent.Agent).
// The agent is launched once and kept alive across rounds; each Review writes a
// new prompt into the same pty so the agent keeps its full context. It reads the
// vt10x-rendered screen for completion detection and verdict parsing.
type AgentDriver struct {
	side     string
	ag       *agent.Agent
	repoDir  string
	wait     agent.WaitOpts
	prompts  *PromptSet
	warmedUp bool
}

// NewAgentDriver wires a driver to an already-started agent.
func NewAgentDriver(side string, ag *agent.Agent, repoDir string, wait agent.WaitOpts, prompts *PromptSet) *AgentDriver {
	return &AgentDriver{side: side, ag: ag, repoDir: repoDir, wait: wait, prompts: prompts}
}

func (d *AgentDriver) Name() string { return d.side }

// trustPromptRe matches the first-run "trust this directory?" / onboarding
// confirmation both agents may show in an unconfigured project.
var trustPromptRe = regexp.MustCompile(`(?i)trust|press enter to continue|yes, (continue|proceed)`)

// warmup waits for the TUI to boot and auto-accepts any first-run trust prompt
// before the first prompt is submitted. Best-effort: on timeout it proceeds.
func (d *AgentDriver) warmup(ctx context.Context) {
	deadline := time.Now().Add(25 * time.Second)
	var stableSince, lastTrust time.Time
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		screen := d.ag.Screen()
		if strings.TrimSpace(screen) == "" {
			continue
		}
		if trustPromptRe.MatchString(screen) {
			_ = d.ag.Write([]byte("\r"))
			lastTrust = time.Now()
			stableSince = time.Time{}
			debugf("%s warmup: accepted trust/onboarding prompt", d.side)
			continue
		}
		if stableSince.IsZero() {
			stableSince = time.Now()
		}
		if time.Since(stableSince) >= 1500*time.Millisecond && time.Since(lastTrust) >= time.Second {
			return
		}
	}
}

// Review submits this side's prompt, waits for the turn to go idle, reads the
// rendered screen, and parses verdict + ask-gate confirmation + diff hash.
func (d *AgentDriver) Review(ctx context.Context, handoff string, ask bool) (Review, error) {
	if !d.warmedUp {
		d.warmup(ctx)
		d.warmedUp = true
	}

	prompt := d.prompts.Render(handoff, ask)
	baseline := d.ag.Screen()
	// Submit in two steps. Both codex and Claude Code debounce fast input as a
	// "paste burst": a long prompt followed immediately by \r makes the TUI treat
	// the \r as a literal newline in the composer, so the prompt is never sent and
	// the turn looks instantly idle. We write the text, let the paste-burst timer
	// flush, THEN send Enter on its own so the TUI registers a real keypress.
	if err := d.ag.Write([]byte(prompt)); err != nil {
		return Review{}, fmt.Errorf("%s submit: %w", d.side, err)
	}
	select {
	case <-ctx.Done():
		return Review{}, ctx.Err()
	case <-time.After(submitEnterDelay):
	}
	if err := d.ag.Write([]byte("\r")); err != nil {
		return Review{}, fmt.Errorf("%s submit-enter: %w", d.side, err)
	}

	screen, err := agent.WaitIdle(ctx, d.wait, baseline, d.ag.Screen)
	if err != nil {
		debugf("%s WaitIdle err=%v", d.side, err)
		dumpScreen(d.side, screen)
		return Review{Side: d.side, Verdict: VerdictUnknown, Report: tailLines(screen, 25)}, nil
	}

	verdict := parseVerdict(screen)
	noMore := parseNoMoreBugs(screen)
	debugf("%s verdict=%s noMoreBugs=%v", d.side, verdict, noMore)
	dumpScreen(d.side, screen)

	hash, herr := gitx.Hash(d.repoDir)
	if herr != nil {
		hash = ""
	}

	rev := Review{
		Side:       d.side,
		Verdict:    verdict,
		Report:     tailLines(screen, 25),
		DiffHash:   hash,
		NoMoreBugs: noMore,
	}

	// Handoff mode: the agent wrote the peer's next-turn prompt (or CONVERGED) to
	// .aibridge/next-<peer>.md. Read it from the file — a reliable channel, unlike
	// scraping the scrolling screen. CONVERGED also counts as a "no more bugs"
	// signal so the existing convergence logic applies unchanged.
	if d.prompts != nil && d.prompts.mode == ModeHandoff {
		peer := peerSide(d.side)
		prompt, converged := readHandoff(d.repoDir, peer)
		rev.HandoffForPeer = prompt
		if converged {
			rev.NoMoreBugs = true
		}
	}

	return rev, nil
}

// parseVerdict takes the LAST AUDIT_RESULT occurrence (the agent writes its real
// verdict after any echoed prompt).
func parseVerdict(screen string) Verdict {
	all := verdictRe.FindAllStringSubmatch(screen, -1)
	if len(all) == 0 {
		return VerdictUnknown
	}
	switch strings.ToUpper(all[len(all)-1][1]) {
	case "CLEAN":
		return VerdictClean
	case "FIXED":
		return VerdictFixed
	case "ISSUES":
		return VerdictIssues
	}
	return VerdictUnknown
}

// parseNoMoreBugs reports the ask-gate confirmation, scanning only the region
// after the last AUDIT_RESULT (the echoed prompt contains the tokens too).
func parseNoMoreBugs(screen string) bool {
	region := afterLastVerdict(screen)
	if hasBareMoreBugs(region) {
		return false
	}
	return noMoreBugsRe.MatchString(region)
}

func afterLastVerdict(screen string) string {
	locs := verdictRe.FindAllStringIndex(screen, -1)
	if len(locs) == 0 {
		return screen
	}
	return screen[locs[len(locs)-1][0]:]
}

// hasBareMoreBugs returns true if "MORE_BUGS" appears not immediately preceded by
// "NO_".
func hasBareMoreBugs(s string) bool {
	for _, loc := range moreBugsRe.FindAllStringIndex(s, -1) {
		start := loc[0]
		if start < 3 || !strings.EqualFold(s[start-3:start], "NO_") {
			return true
		}
	}
	return false
}

// tailLines returns the last n non-empty lines of s.
func tailLines(s string, n int) string {
	var lines []string
	for ln := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, strings.TrimRight(ln, " "))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
