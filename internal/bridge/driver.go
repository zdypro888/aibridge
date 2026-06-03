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

	handoffMode := d.prompts != nil && d.prompts.mode == ModeHandoff
	peer := peerSide(d.side)
	// Clear any stale peer-handoff file so we only ever read what THIS turn wrote.
	if handoffMode {
		clearHandoff(d.repoDir, peer)
	}

	prompt := d.prompts.Render(handoff, ask)
	screen, err := d.submitAndWait(ctx, prompt)
	if err != nil {
		debugf("%s WaitIdle err=%v", d.side, err)
		dumpScreen(d.side, screen)
		return Review{Side: d.side, Verdict: VerdictUnknown, Report: tailLines(screen, 25)}, nil
	}

	verdict := parseVerdict(screen)
	noMore := parseNoMoreBugs(screen)
	hoPrompt, converged := "", false
	if handoffMode {
		hoPrompt, converged = readHandoff(d.repoDir, peer)
	}

	// Completion nudge: a long turn can trigger the CLI's context compaction,
	// which drops the turn-start instructions — so the agent may finish without
	// the machine-required output (verdict line; in handoff mode the peer file).
	// Re-prompt with a SHORT, self-contained reminder (survives a shrunken
	// context) and re-read, up to maxNudges times.
	const maxNudges = 2
	for n := range maxNudges {
		needVerdict := verdict == VerdictUnknown
		needFile := handoffMode && hoPrompt == "" && !converged
		if !needVerdict && !needFile {
			break
		}
		debugf("%s incomplete (needVerdict=%v needFile=%v); nudge %d", d.side, needVerdict, needFile, n+1)
		nudge := completionNudge(d.prompts.lang, peer, needVerdict, needFile)
		screen, err = d.submitAndWait(ctx, nudge)
		if err != nil {
			debugf("%s nudge WaitIdle err=%v", d.side, err)
			break
		}
		if v := parseVerdict(screen); v != VerdictUnknown {
			verdict = v
		}
		if parseNoMoreBugs(screen) {
			noMore = true
		}
		if handoffMode {
			if p, c := readHandoff(d.repoDir, peer); p != "" || c {
				hoPrompt, converged = p, c
			}
		}
	}
	dumpScreen(d.side, screen)
	debugf("%s verdict=%s noMoreBugs=%v converged=%v", d.side, verdict, noMore, converged)

	hash, herr := gitx.Hash(d.repoDir)
	if herr != nil {
		hash = ""
	}

	rev := Review{
		Side:           d.side,
		Verdict:        verdict,
		Report:         tailLines(screen, 25),
		DiffHash:       hash,
		NoMoreBugs:     noMore || converged, // CONVERGED folds into the no-more-bugs signal
		HandoffForPeer: hoPrompt,
	}
	return rev, nil
}

// submitAndWait writes text to the agent's pty (two-step to defeat paste-burst
// debounce: text, pause, then Enter alone) and blocks until the turn goes idle.
func (d *AgentDriver) submitAndWait(ctx context.Context, text string) (string, error) {
	// Capture the baseline BEFORE submitting: WaitIdle detects the turn started by
	// the screen diverging from this pre-submit snapshot.
	baseline := d.ag.Screen()
	if err := d.ag.Write([]byte(text)); err != nil {
		return "", fmt.Errorf("%s submit: %w", d.side, err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(submitEnterDelay):
	}
	if err := d.ag.Write([]byte("\r")); err != nil {
		return "", fmt.Errorf("%s submit-enter: %w", d.side, err)
	}
	return agent.WaitIdle(ctx, d.wait, baseline, d.ag.Screen)
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
