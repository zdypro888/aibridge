package bridge

import (
	"context"
	"fmt"
	"testing"
)

// scriptDriver is a fake agent. Each Review pops the next scripted step, which
// says what verdict to return, whether to confirm no-more-bugs, and (optionally)
// mutates the shared fake hash to simulate editing code.
type scriptDriver struct {
	name  string
	steps []step
	i     int
	hp    *string // shared pointer to the fake work-tree hash
}

type step struct {
	verdict Verdict
	newHash string // if non-empty, simulate a code edit by setting the shared hash
	noMore  bool   // ask-gate confirmation this turn
}

func (d *scriptDriver) Name() string { return d.name }

func (d *scriptDriver) Review(_ context.Context, _ string, _ bool) (Review, error) {
	if d.i >= len(d.steps) {
		// Default: no change, clean, confirms no more bugs (lets a driver outlast
		// the other side without breaking convergence).
		return Review{Side: d.name, Verdict: VerdictClean, NoMoreBugs: true, DiffHash: *d.hp}, nil
	}
	s := d.steps[d.i]
	d.i++
	if s.newHash != "" {
		*d.hp = s.newHash
	}
	return Review{Side: d.name, Verdict: s.verdict, NoMoreBugs: s.noMore, DiffHash: *d.hp}, nil
}

func mkHasher(hp *string) Hasher {
	return func() (string, error) { return *hp, nil }
}

func run(cfg Config, codex, claude Driver, hp *string) (Outcome, error) {
	return Run(context.Background(), cfg, Deps{Codex: codex, Claude: claude, Hash: mkHasher(hp)})
}

func TestDiffFixpoint_ConvergesWhenBothCleanNoChange(t *testing.T) {
	h := "base"
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{{verdict: VerdictClean}}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{{verdict: VerdictClean}}}

	out, err := run(Config{MaxRounds: 6, FirstSide: "codex", Strategy: "diff-fixpoint"}, codex, claude, &h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Converged {
		t.Fatalf("expected convergence, got reason=%q rounds=%d", out.Reason, out.Rounds)
	}
	if out.Rounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", out.Rounds)
	}
}

func TestDiffFixpoint_ConvergesAfterFixThenAgreement(t *testing.T) {
	h := "base"
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{
		{verdict: VerdictFixed, newHash: "fixed1"},
		{verdict: VerdictClean},
	}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{{verdict: VerdictClean}}}

	out, err := run(Config{MaxRounds: 6, FirstSide: "codex", Strategy: "diff-fixpoint"}, codex, claude, &h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Converged {
		t.Fatalf("expected convergence; reason=%q trail=%+v", out.Reason, out.Trail)
	}
	if out.Rounds != 3 {
		t.Fatalf("expected 3 rounds, got %d", out.Rounds)
	}
}

func TestNoConverge_WhenSidesKeepEditing(t *testing.T) {
	h := "base"
	mk := func(name, prefix string) *scriptDriver {
		var steps []step
		for i := range 10 {
			steps = append(steps, step{verdict: VerdictFixed, newHash: fmt.Sprintf("%s-%d", prefix, i)})
		}
		return &scriptDriver{name: name, hp: &h, steps: steps}
	}
	out, err := run(Config{MaxRounds: 4, FirstSide: "codex", Strategy: "diff-fixpoint"}, mk("codex", "cx"), mk("claude", "cl"), &h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Converged {
		t.Fatalf("expected NO convergence when both keep editing")
	}
	if out.Rounds != 4 {
		t.Fatalf("expected to hit max 4 rounds, got %d", out.Rounds)
	}
}

func TestAskGate_RequiresBothConfirm(t *testing.T) {
	h := "base"
	// Both clean+no change, but ask-gate needs explicit NO_MORE_BUGS twice.
	// codex confirms, claude confirms -> converge in 2.
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{{verdict: VerdictClean, noMore: true}}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{{verdict: VerdictClean, noMore: true}}}
	out, err := run(Config{MaxRounds: 6, FirstSide: "codex", Strategy: "ask-gate"}, codex, claude, &h)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !out.Converged || out.Rounds != 2 {
		t.Fatalf("ask-gate expected converge in 2; got converged=%v rounds=%d", out.Converged, out.Rounds)
	}
}

func TestAskGate_NoConvergeWithoutConfirm(t *testing.T) {
	h := "base"
	// Clean verdicts but agents keep saying MORE_BUGS (noMore=false) -> never converge.
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
	}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
	}}
	out, _ := run(Config{MaxRounds: 4, FirstSide: "codex", Strategy: "ask-gate"}, codex, claude, &h)
	if out.Converged {
		t.Fatalf("ask-gate should not converge without NO_MORE_BUGS confirmation")
	}
}

func TestCombined_NeedsBothSignals(t *testing.T) {
	h := "base"
	// Clean + no change but no confirmation -> combined must NOT converge
	// (diff-fixpoint alone would have).
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
	}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
		{verdict: VerdictClean, noMore: false}, {verdict: VerdictClean, noMore: false},
	}}
	out, _ := run(Config{MaxRounds: 4, FirstSide: "codex", Strategy: "combined"}, codex, claude, &h)
	if out.Converged {
		t.Fatalf("combined should require BOTH diff-fixpoint and ask confirmation")
	}
}

func TestUnlimitedRounds_NotCappedButConverges(t *testing.T) {
	h := "base"
	// codex keeps editing for 12 turns (well past the old default cap of 8),
	// then both go quiet. With MaxRounds=0 (unlimited) the loop must NOT stop on a
	// round cap; it runs until the scripts are exhausted and the scriptDriver's
	// default (clean + no-more-bugs, no change) lets it converge.
	var steps []step
	for i := range 12 {
		steps = append(steps, step{verdict: VerdictFixed, newHash: fmt.Sprintf("e-%d", i)})
	}
	codex := &scriptDriver{name: "codex", hp: &h, steps: steps}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{}}

	out, err := run(Config{MaxRounds: 0, FirstSide: "codex", Strategy: "combined"}, codex, claude, &h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Converged {
		t.Fatalf("unlimited run should converge once edits stop; reason=%q rounds=%d", out.Reason, out.Rounds)
	}
	if out.Rounds <= 8 {
		t.Fatalf("unlimited run should run past the old cap of 8; got %d rounds", out.Rounds)
	}
}

func TestControl_OnlySide(t *testing.T) {
	h := "base"
	codex := &scriptDriver{name: "codex", hp: &h, steps: []step{{verdict: VerdictClean, noMore: true}}}
	claude := &scriptDriver{name: "claude", hp: &h, steps: []step{{verdict: VerdictClean, noMore: true}}}
	ctrl := NewControl()
	ctrl.OnlySide("codex") // claude turns are skipped

	// With only codex running and ask-gate needing 2 confirmations from
	// consecutive turns, codex confirms each of its turns; claude turns are
	// skipped (counted as rounds). Should hit max rounds without 2 consecutive
	// codex confirmations (claude's skipped turn sits between them).
	out, err := Run(context.Background(), Config{MaxRounds: 4, FirstSide: "codex", Strategy: "ask-gate"},
		Deps{Codex: codex, Claude: claude, Hash: mkHasher(&h), Ctrl: ctrl})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Verify claude never actually reviewed (its step counter untouched).
	if claude.i != 0 {
		t.Fatalf("claude should have been skipped by OnlySide, but ran %d times", claude.i)
	}
	if out.Converged {
		t.Fatalf("only-side control should not let one side satisfy both-side convergence; rounds=%d reason=%q", out.Rounds, out.Reason)
	}
	if out.Rounds != 4 {
		t.Fatalf("expected to hit max rounds, got %d", out.Rounds)
	}
}
