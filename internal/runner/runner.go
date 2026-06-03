// Package runner turns a config into a live bridge run: it launches each enabled
// agent on a pty, builds drivers with the configured prompt templates and
// completion settings, and drives the ping-pong loop while exposing an event bus
// (for the UI to watch), a control surface (for the UI to steer), and the live
// agents (for the web terminal to attach to).
package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"aibridge/internal/agent"
	"aibridge/internal/bridge"
	"aibridge/internal/config"
	"aibridge/internal/gitx"
	"aibridge/internal/promptlib"
)

// Resume controls whether one agent continues a previous CLI session instead of
// starting fresh. SessionID empty + Enabled true means "the most recent session
// for this repo".
type Resume struct {
	Enabled   bool
	SessionID string
}

// ResumeSet carries per-agent resume choices for a run.
type ResumeSet struct {
	Codex  Resume
	Claude Resume
}

// applyResume rewrites an agent launch command so it continues a prior session.
// The two CLIs differ: claude takes a flag (--resume <id> / --continue), codex
// uses a subcommand inserted right after the binary (resume <id> / resume
// --last). A disabled resume returns the command unchanged.
func applyResume(side, command string, r Resume) string {
	if !r.Enabled {
		return command
	}
	switch side {
	case "claude":
		if r.SessionID != "" {
			return command + " --resume " + shellQuote(r.SessionID)
		}
		return command + " --continue"
	case "codex":
		first, rest, _ := strings.Cut(strings.TrimSpace(command), " ")
		sub := "resume --last"
		if r.SessionID != "" {
			sub = "resume " + shellQuote(r.SessionID)
		}
		if strings.TrimSpace(rest) != "" {
			return first + " " + sub + " " + rest
		}
		return first + " " + sub
	default:
		return command
	}
}

// shellQuote single-quotes a value for safe inclusion in the `sh -c` command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// busyRe compiles an agent's busy-status pattern, falling back to the built-in
// default when empty. Config.Validate already rejected an invalid custom regexp,
// so a compile error here can only be a bad default — in which case we return nil
// (Timeout still backstops) rather than panic.
func busyRe(pattern string) *regexp.Regexp {
	if pattern == "" {
		pattern = config.DefaultBusyPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// terminalCols/Rows is the initial pty/emulator size; the browser resizes it to
// fit its panel once xterm has measured itself.
const (
	terminalCols = 120
	terminalRows = 40
)

// Runner owns one bridge run's lifecycle and is safe for the server to query
// concurrently while a run is in progress.
type Runner struct {
	bus  *bridge.Bus
	ctrl *bridge.Control
	hub  *bridge.MCPHub // routes agents' submit_review MCP calls to the waiting driver

	mu      sync.Mutex
	running bool
	last    *bridge.Outcome
	cancel  context.CancelFunc
	agents  map[string]*agent.Agent // side -> live agent (for web terminal attach)
}

// New creates an idle runner with a fresh event bus and control surface.
func New() *Runner {
	return &Runner{bus: bridge.NewBus(500), ctrl: bridge.NewControl(), hub: bridge.NewMCPHub(), agents: map[string]*agent.Agent{}}
}

func (r *Runner) Bus() *bridge.Bus { return r.bus }

// Hub returns the MCP hub the HTTP /mcp endpoint delivers tool calls to.
func (r *Runner) Hub() *bridge.MCPHub { return r.hub }

// Control returns the current control surface. Synchronized because Start swaps
// in a fresh *Control per run while the HTTP layer may concurrently fetch it.
func (r *Runner) Control() *bridge.Control {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctrl
}

// Agent returns the live agent for a side, or nil if no run is active / the side
// is disabled. The web terminal subscribes to it.
func (r *Runner) Agent(side string) *agent.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agents[side]
}

// Running reports whether a run is in progress.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// LastOutcome returns the most recent finished run's result, or nil.
func (r *Runner) LastOutcome() *bridge.Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Start validates the config and launches a run in the background using the
// given prompt template. It returns an error synchronously for setup failures
// (bad config, not a repo, agent launch).
func (r *Runner) Start(cfg config.Config, tmpl promptlib.Template, resume ResumeSet) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("a run is already in progress")
	}
	preselectedOnlySide, _ := r.ctrl.State()["onlySide"].(string)
	r.running = true
	r.mu.Unlock()

	clearRunning := func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}

	if err := cfg.Validate(); err != nil {
		clearRunning()
		return err
	}
	if !gitx.IsRepo(cfg.Repo) {
		clearRunning()
		return fmt.Errorf("%s is not a git work tree", cfg.Repo)
	}

	// Prepare the handoff exchange dir and clear any stale handoff files from a
	// previous run so the first turn never reads an old peer prompt.
	bridge.PrepareHandoff(cfg.Repo)

	// In MCP mode, write the per-repo MCP client config so each CLI connects back
	// to our /mcp endpoint. Repo-scoped only — never touches global user config.
	if bridge.ReviewMode(cfg.Flow.ReviewMode) == bridge.ModeMCP {
		r.hub.Reset() // clear last run's handoff cache
		if err := bridge.WriteMCPConfig(cfg.Repo, cfg.Server.Addr); err != nil {
			clearRunning()
			return fmt.Errorf("write mcp config: %w", err)
		}
	}

	codexDrv, claudeDrv, agents, cleanup, err := r.buildDrivers(cfg, tmpl, resume)
	if err != nil {
		clearRunning()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctrl := bridge.NewControl()
	if preselectedOnlySide != "" {
		ctrl.OnlySide(preselectedOnlySide)
	}
	r.mu.Lock()
	r.cancel = cancel
	r.ctrl = ctrl // fresh control per run
	r.agents = agents
	r.mu.Unlock()

	go func() {
		defer cleanup()
		out, _ := bridge.Run(ctx, bridge.Config{
			MaxRounds: cfg.Flow.MaxRounds,
			FirstSide: cfg.Flow.First,
			Strategy:  cfg.Flow.Strategy,
		}, bridge.Deps{
			Codex:  codexDrv,
			Claude: claudeDrv,
			Hash:   func() (string, error) { return gitx.Hash(cfg.Repo) },
			Bus:    r.bus,
			Ctrl:   ctrl,
		})
		r.mu.Lock()
		r.running = false
		r.last = &out
		r.cancel = nil
		r.agents = map[string]*agent.Agent{}
		r.mu.Unlock()
	}()
	return nil
}

// Stop aborts the in-progress run, if any.
func (r *Runner) Stop() {
	r.mu.Lock()
	c := r.cancel
	ctrl := r.ctrl
	r.mu.Unlock()
	ctrl.Abort()
	if c != nil {
		c()
	}
}

// buildDrivers launches a pty-backed agent for each enabled side and wires
// drivers. A disabled agent gets a no-op driver so the loop's alternation still
// works (its turns are skipped via a clean, no-change review).
func (r *Runner) buildDrivers(cfg config.Config, tmpl promptlib.Template, resume ResumeSet) (codex, claude bridge.Driver, agents map[string]*agent.Agent, cleanup func(), err error) {
	agents = map[string]*agent.Agent{}
	cleanup = func() {
		for _, a := range agents {
			_ = a.Kill()
		}
	}

	mk := func(side string, ac config.Agent) (bridge.Driver, error) {
		if !ac.Enabled {
			return noopDriver{side: side}, nil
		}
		sp := tmpl.Codex
		if side == "claude" {
			sp = tmpl.Claude
		}
		ps, perr := bridge.NewPromptSet(bridge.ReviewKind(tmpl.ReviewKind()), side, sp.First, sp.Next, cfg.Lang, tmpl.Ask)
		if perr != nil {
			return nil, fmt.Errorf("%s prompt template: %w", side, perr)
		}
		ps.SetMode(bridge.ReviewMode(cfg.Flow.ReviewMode))
		res := resume.Codex
		if side == "claude" {
			res = resume.Claude
		}
		command := applyResume(side, ac.Command, res)
		ag := agent.New(side)
		if serr := ag.Start(cfg.Repo, command, terminalCols, terminalRows); serr != nil {
			return nil, fmt.Errorf("start %s: %w", side, serr)
		}
		agents[side] = ag
		wait := agent.WaitOpts{
			Poll:    ac.Poll.D(),
			Stable:  ac.StableFor.D(),
			Settle:  ac.SettleFor.D(),
			Timeout: ac.Timeout.D(),
			Busy:    busyRe(ac.BusyPattern),
		}
		drv := bridge.NewAgentDriver(side, ag, cfg.Repo, wait, ps)
		if bridge.ReviewMode(cfg.Flow.ReviewMode) == bridge.ModeMCP {
			drv.SetHub(r.hub)
		}
		return drv, nil
	}

	codex, err = mk("codex", cfg.Agents.Codex)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, err
	}
	claude, err = mk("claude", cfg.Agents.Claude)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, err
	}
	return codex, claude, agents, cleanup, nil
}

// noopDriver stands in for a disabled agent: it never changes code and always
// reports clean + no-more-bugs, so a single-agent run converges via the other
// side's signals without this one blocking.
type noopDriver struct{ side string }

func (n noopDriver) Name() string { return n.side }
func (n noopDriver) Review(_ context.Context, _ string, _ bool) (bridge.Review, error) {
	return bridge.Review{Side: n.side, Verdict: bridge.VerdictClean, NoMoreBugs: true}, nil
}
