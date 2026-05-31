package bridge

// Strategy decides, after each turn, whether the run has converged ("both sides
// agree there are no more bugs"). It is pluggable so the flow is not hard-wired
// to one definition of "done". The loop feeds each completed turn to the active
// strategy and stops when Converged returns true (or max rounds is hit).
//
// Implementations are stateful across a run (they accumulate streaks) and are
// created fresh per run via the factory below.
type Strategy interface {
	// Name is the configured identifier.
	Name() string
	// Observe records one completed turn. changed reports whether the work-tree
	// diff hash moved during that turn.
	Observe(rev Review, changed bool)
	// Converged reports whether the run is done given everything observed so far.
	Converged() bool
	// Reason explains the current convergence decision for logs/UI.
	Reason() string
	// NeedsAsk reports whether, for THIS turn, the agent's prompt should include
	// the "are there any more bugs?" question (ask-gate / combined strategies).
	// The loop uses this to decide which prompt variant to send.
	NeedsAsk() bool
}

// NewStrategy builds the configured convergence strategy. askToken/clearToken are
// the verdict-style tokens the ask-gate looks for in the agent's reply.
func NewStrategy(name string) Strategy {
	switch name {
	case "ask-gate":
		return &askGate{}
	case "diff-fixpoint":
		return &diffFixpoint{}
	default: // "combined"
		return &combined{
			diff: &diffFixpoint{},
			ask:  &askGate{},
		}
	}
}

// diffFixpoint converges when a full round passes with no code change and the
// latest verdict is CLEAN, twice in a row (one clean turn per side). This is the
// original "both clean, nobody edited" rule.
type diffFixpoint struct {
	cleanStreak int
	reason      string
}

func (d *diffFixpoint) Name() string   { return "diff-fixpoint" }
func (d *diffFixpoint) NeedsAsk() bool { return false }

func (d *diffFixpoint) Observe(rev Review, changed bool) {
	switch {
	case changed:
		d.cleanStreak = 0
		d.reason = "code changed; not converged"
	case rev.Verdict == VerdictClean:
		d.cleanStreak++
		d.reason = "clean turn with no code change"
	default:
		d.cleanStreak = 0
		d.reason = "no change but verdict not clean"
	}
}

func (d *diffFixpoint) Converged() bool { return d.cleanStreak >= 2 }
func (d *diffFixpoint) Reason() string {
	if d.Converged() {
		return "double-clean: both sides reviewed with no further code changes"
	}
	return d.reason
}

// askGate converges only when both sides, on consecutive turns, explicitly answer
// "no more bugs". The agent's reply is expected to carry NO_MORE_BUGS (parsed by
// the loop into Review.NoMoreBugs). This adds a semantic confirmation on top of
// (or instead of) the diff signal — the "are you sure there are no other bugs?"
// gate the user asked for.
type askGate struct {
	noStreak int
	reason   string
}

func (a *askGate) Name() string   { return "ask-gate" }
func (a *askGate) NeedsAsk() bool { return true }

func (a *askGate) Observe(rev Review, _ bool) {
	if rev.NoMoreBugs {
		a.noStreak++
		a.reason = "agent confirmed no more bugs"
	} else {
		a.noStreak = 0
		a.reason = "agent did not confirm clean (or found more bugs)"
	}
}

func (a *askGate) Converged() bool { return a.noStreak >= 2 }
func (a *askGate) Reason() string {
	if a.Converged() {
		return "both sides confirmed no remaining bugs"
	}
	return a.reason
}

// combined requires BOTH signals: no code churn AND explicit "no more bugs"
// confirmation, both holding for a full round. Strictest and the default — it's
// the most robust answer to "are we really done?".
type combined struct {
	diff *diffFixpoint
	ask  *askGate
}

func (c *combined) Name() string   { return "combined" }
func (c *combined) NeedsAsk() bool { return true }

func (c *combined) Observe(rev Review, changed bool) {
	c.diff.Observe(rev, changed)
	c.ask.Observe(rev, changed)
}

func (c *combined) Converged() bool { return c.diff.Converged() && c.ask.Converged() }
func (c *combined) Reason() string {
	if c.Converged() {
		return "double-clean and both sides confirmed no remaining bugs"
	}
	return c.diff.Reason() + "; " + c.ask.Reason()
}
