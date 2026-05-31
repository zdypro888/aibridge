package bridge

import (
	"context"
	"fmt"
)

// Config tunes the ping-pong loop.
type Config struct {
	MaxRounds int    // hard ceiling on turns; 0 (or negative) means unlimited — run until convergence or abort
	FirstSide string // who reviews first, "codex" or "claude"
	Strategy  string // convergence strategy name (see NewStrategy)
}

// DefaultConfig: codex first, combined convergence, capped at 8 turns.
func DefaultConfig() Config {
	return Config{MaxRounds: 8, FirstSide: "codex", Strategy: "combined"}
}

// Outcome reports how the loop ended.
type Outcome struct {
	Converged bool     // true if the strategy reached agreement
	Rounds    int      // number of turns actually taken
	Reason    string   // human-readable explanation of why the loop stopped
	Trail     []Review // every turn in order
}

// Deps are the runtime collaborators the loop needs. bus and ctrl are optional
// (nil-safe) so the loop can run headless in tests.
type Deps struct {
	Codex  Driver
	Claude Driver
	Hash   Hasher
	Bus    *Bus
	Ctrl   *Control
}

// Run drives the mutual-review loop until the convergence strategy is satisfied,
// the round cap is hit, or the user aborts. Convergence is decided by the
// pluggable Strategy, not a hard-wired rule.
func Run(ctx context.Context, cfg Config, d Deps) (Outcome, error) {
	// MaxRounds <= 0 means unlimited: the loop runs until the strategy converges
	// or the user aborts. This is what the whole-codebase review needs ("keep
	// going until there is nothing left to improve").
	unlimited := cfg.MaxRounds <= 0
	strat := NewStrategy(cfg.Strategy)
	bus := d.Bus
	ctrl := d.Ctrl

	order := []Driver{d.Codex, d.Claude}
	if cfg.FirstSide == "claude" {
		order = []Driver{d.Claude, d.Codex}
	}

	out := Outcome{}
	var lastHandoff string

	prevHash, err := d.Hash()
	if err != nil {
		return out, fmt.Errorf("initial hash: %w", err)
	}

	maxRoundsMsg := fmt.Sprintf("%d", cfg.MaxRounds)
	if unlimited {
		maxRoundsMsg = "unlimited"
	}
	publish(bus, Event{Kind: EventRunStarted, Message: fmt.Sprintf("strategy=%s first=%s maxRounds=%s", strat.Name(), cfg.FirstSide, maxRoundsMsg)})

	for unlimited || out.Rounds < cfg.MaxRounds {
		drv := order[out.Rounds%2]
		side := drv.Name()

		if ctrl != nil {
			allowed, injected, abErr := ctrl.gateBeforeTurn(ctx, side)
			if abErr != nil {
				out.Reason = abErr.Error()
				publish(bus, Event{Kind: EventStopped, Message: out.Reason})
				return out, nil
			}
			if !allowed {
				// Side disabled/skipped this turn. Count it so MaxRounds still caps
				// single-agent control modes, and explicitly break convergence
				// streaks so one active side cannot satisfy a "both sides" strategy
				// by itself.
				out.Rounds++
				strat.Observe(Review{Side: side, Verdict: VerdictUnknown, DiffHash: prevHash}, false)
				publish(bus, Event{Kind: EventLog, Side: side, Message: "turn skipped"})
				continue
			}
			if injected != "" {
				lastHandoff = injected + "\n" + lastHandoff
			}
		}

		ask := strat.NeedsAsk()
		publish(bus, Event{Kind: EventTurnStarted, Side: side, Round: out.Rounds + 1})

		rev, rerr := drv.Review(ctx, lastHandoff, ask)
		if rerr != nil {
			out.Reason = fmt.Sprintf("%s review failed: %v", side, rerr)
			publish(bus, Event{Kind: EventStopped, Message: out.Reason})
			return out, rerr
		}
		out.Rounds++

		if rev.DiffHash == "" {
			if h, herr := d.Hash(); herr == nil {
				rev.DiffHash = h
			}
		}
		out.Trail = append(out.Trail, rev)

		changed := rev.DiffHash != prevHash
		prevHash = rev.DiffHash
		lastHandoff = buildHandoff(rev, changed)

		strat.Observe(rev, changed)

		publish(bus, Event{
			Kind: EventTurnFinished, Side: side, Round: out.Rounds,
			Verdict: string(rev.Verdict),
			Message: strat.Reason(),
		})

		if strat.Converged() {
			out.Converged = true
			out.Reason = strat.Reason()
			publish(bus, Event{Kind: EventConverged, Round: out.Rounds, Message: out.Reason})
			return out, nil
		}
	}

	out.Reason = fmt.Sprintf("hit max rounds (%d) without convergence", cfg.MaxRounds)
	publish(bus, Event{Kind: EventStopped, Message: out.Reason})
	return out, nil
}

// buildHandoff produces a SHORT, single-line note for the next agent. It must
// stay short: it becomes part of the prompt typed into the next agent's TUI, and
// a single typed line longer than the tty canonical-input limit (~4096 bytes) is
// silently dropped by the line discipline. Agents exchange detail through the
// shared git work tree (each reads `git diff`), not through this note.
func buildHandoff(rev Review, changed bool) string {
	changeNote := "they did not change the code"
	if changed {
		changeNote = "they changed the code — review their edits"
	}
	return fmt.Sprintf("Previous reviewer was %s; verdict %s; %s.", rev.Side, rev.Verdict, changeNote)
}

func publish(bus *Bus, e Event) {
	if bus != nil {
		bus.Publish(e)
	}
}
