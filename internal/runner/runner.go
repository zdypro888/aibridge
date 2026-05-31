// Package runner turns a config into a live bridge run: it launches each enabled
// agent on a pty, builds drivers with the configured prompt templates and
// completion settings, and drives the ping-pong loop while exposing an event bus
// (for the UI to watch), a control surface (for the UI to steer), and the live
// agents (for the web terminal to attach to).
package runner

import (
	"context"
	"fmt"
	"sync"

	"aibridge/internal/agent"
	"aibridge/internal/bridge"
	"aibridge/internal/config"
	"aibridge/internal/gitx"
)

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

	mu      sync.Mutex
	running bool
	last    *bridge.Outcome
	cancel  context.CancelFunc
	agents  map[string]*agent.Agent // side -> live agent (for web terminal attach)
}

// New creates an idle runner with a fresh event bus and control surface.
func New() *Runner {
	return &Runner{bus: bridge.NewBus(500), ctrl: bridge.NewControl(), agents: map[string]*agent.Agent{}}
}

func (r *Runner) Bus() *bridge.Bus { return r.bus }

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

// Start validates the config and launches a run in the background. It returns an
// error synchronously for setup failures (bad config, not a repo, agent launch).
func (r *Runner) Start(cfg config.Config) error {
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

	codexDrv, claudeDrv, agents, cleanup, err := r.buildDrivers(cfg)
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
func (r *Runner) buildDrivers(cfg config.Config) (codex, claude bridge.Driver, agents map[string]*agent.Agent, cleanup func(), err error) {
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
		ps, perr := bridge.NewPromptSet(side, ac.PromptFirst, ac.PromptNext, cfg.Lang, cfg.Flow.AskPrompt)
		if perr != nil {
			return nil, fmt.Errorf("%s prompt template: %w", side, perr)
		}
		ag := agent.New(side)
		if serr := ag.Start(cfg.Repo, ac.Command, terminalCols, terminalRows); serr != nil {
			return nil, fmt.Errorf("start %s: %w", side, serr)
		}
		agents[side] = ag
		wait := agent.WaitOpts{
			Poll:    ac.Poll.D(),
			Stable:  ac.StableFor.D(),
			Settle:  ac.SettleFor.D(),
			Timeout: ac.Timeout.D(),
		}
		return bridge.NewAgentDriver(side, ag, cfg.Repo, wait, ps), nil
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
