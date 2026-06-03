// Package bridge holds the orchestration logic: the ping-pong loop between the
// two agents and the pluggable convergence decision. It is transport-agnostic —
// it talks to each agent through the Driver interface so the same loop runs
// against real PTY-backed agents or mock agents in tests.
package bridge

import "context"

// Verdict is an agent's judgement of the current changes after it reviewed them.
type Verdict string

const (
	VerdictClean   Verdict = "CLEAN"   // no problems found
	VerdictFixed   Verdict = "FIXED"   // found problems and edited the code to fix them
	VerdictIssues  Verdict = "ISSUES"  // found problems but did not fix (left for the other side)
	VerdictUnknown Verdict = "UNKNOWN" // could not parse a verdict from the agent's output
)

// Review is the result of one agent taking one turn.
type Review struct {
	Side       string  // "codex" or "claude"
	Verdict    Verdict // parsed from the agent's final message
	Report     string  // the agent's prose output (relayed to the other side / shown to user)
	DiffHash   string  // hash of the work tree AFTER this turn
	NoMoreBugs bool    // ask-gate: the agent explicitly confirmed there are no remaining bugs
	// HandoffForPeer, in handoff mode, is the free-form next-turn prompt this
	// agent wrote (to .aibridge/next-<peer>.md) for the OTHER agent. Empty in
	// other modes or if the agent wrote nothing. When set, the loop feeds it to
	// the peer's next turn instead of the generated short note.
	HandoffForPeer string
}

// Driver is one reviewer the bridge can hand work to and read a result back from.
// Implemented by a PTY-backed agent in production and by a fake in tests.
type Driver interface {
	// Name identifies the side ("codex" / "claude") for logging and reports.
	Name() string
	// Review asks the agent to review the current changes. handoff is the short
	// note from the other side; ask tells the driver to include the
	// "any more bugs?" question this turn. The agent may edit the shared work
	// tree. Blocks until the turn is complete.
	Review(ctx context.Context, handoff string, ask bool) (Review, error)
}

// Hasher returns the current work-tree diff hash. Injected so tests can drive
// convergence deterministically without a real repo.
type Hasher func() (string, error)
