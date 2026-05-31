# aibridge

A product for **mutual code review between two live, interactive AI agents** —
Claude Code and OpenAI Codex. They run in live PTY-backed CLI sessions and take turns reviewing
the shared git work tree: one edits, the other audits the diff, ping-ponging
until both agree the code is clean. Driven from a **web dashboard** where you
watch both agents work live, edit every setting, switch convergence strategies,
and steer the run.

```
go build -o aibridge ./cmd/aibridge
./aibridge --repo /path/to/your/git/repo
# opens http://127.0.0.1:8799 — press Start
```

## Why interactive (not SDK / -p / exec)

Both agents stay real interactive sessions, driven through pseudo-terminals with
raw output streamed to the dashboard. This avoids the non-interactive paths
(`claude -p`, `codex exec`, the Agent/Codex SDKs) which draw from separate,
rate-limited credit pools (Anthropic split SDK/`-p` into its own pool on
2026-06-15; the Codex SDK is `codex exec` under the hood). Interactive CLI
sessions use your normal subscription path. The two agents communicate only
through the shared git diff.

## Web dashboard

- **Live terminals**: both agents' raw PTY output, streamed over WebSockets.
- **Verdict & timeline**: each turn's CLEAN/FIXED/ISSUES and convergence reason.
- **Config editor** (⚙): every setting below, saved to `aibridge.yaml`.
- **Steering**: Start / Stop / Pause / Resume; restrict to one agent (codex-only
  / claude-only / both); skip a side's next turn; inject a message into either
  agent before its next turn.

## Configurable (everything)

`aibridge.yaml` (created on first save; zero-config defaults otherwise):

```yaml
repo: /path/to/repo
agents:
  codex:
    enabled: true
    command: "/Applications/Codex.app/Contents/Resources/codex"
    prompt_first: ""        # Go template; empty = built-in default
    prompt_next:  ""        # may reference {{.Handoff}} {{.Ask}} {{.Verdict}} {{.AskBlock}}
    stable_for: 10s         # screen-quiet duration that counts a turn "done"
    settle_for: 30s         # max wait for the response to start rendering
    timeout: 10m
  claude:
    enabled: true
    command: "claude --dangerously-skip-permissions"
    # ...same fields...
flow:
  first: codex              # who reviews first
  max_rounds: 8
  strategy: combined        # convergence strategy (below)
server:
  addr: 127.0.0.1:8799
```

## Convergence strategies (pluggable, not hard-wired)

The "are we done / any more bugs?" decision is a swappable strategy:

| strategy        | converges when… |
|-----------------|-----------------|
| `diff-fixpoint` | a full round passes with no code change and both verdicts CLEAN |
| `ask-gate`      | both sides explicitly answer **NO_MORE_BUGS** on consecutive turns |
| `combined` (default) | BOTH of the above hold — strictest; the real "are you sure there are no other bugs?" gate |

Agents emit `AUDIT_RESULT: CLEAN|FIXED|ISSUES`, and (for ask-gate/combined)
`NO_MORE_BUGS` / `MORE_BUGS`, parsed from the captured screen.

## Single-agent mode

Disable one agent (`enabled: false`) or use the dashboard's codex-only /
claude-only buttons to drive just one AI.

## Architecture

```
cmd/aibridge      CLI entry: web dashboard (default) or --headless one-shot
internal/config   YAML config + validation, human-readable durations
internal/agent    PTY process control (start/input/screen) + idle detection
internal/bridge   ping-pong loop, pluggable Strategy, prompt templates,
                  PTY driver, event bus, control surface
internal/runner   config → launch agents → drive loop, expose bus + control
internal/server   REST + SSE events + WebSocket terminals + embedded dashboard
cmd/mockagent     fake interactive agent for hermetic e2e tests
```

## CLI flags

`--config` (path, default aibridge.yaml) · `--repo` / `--addr` (override config) ·
`--headless` (run one loop in the terminal, no UI) · `--no-open` (don't launch
browser). `AIBRIDGE_DEBUG=1` prints per-turn parse traces.

## Verified

- 14 unit tests: all three convergence strategies, verdict & NO_MORE_BUGS
  parsing (incl. ignoring the echoed prompt's own tokens), prompt template
  rendering (single-line, custom, ask-block, malformed), and single-agent control.
- PTY e2e with mock agents: converges `[codex:FIXED claude:CLEAN codex:CLEAN]`.
- **Real live run**: Claude (Opus) ↔ Codex (gpt-5.5) on a buggy repo converged
  to double-clean; codex fixed an off-by-one and a divide-by-zero, claude audited
  and confirmed. Server-driven run over the web API, SSE, and WebSockets verified end to end.

## Known edge cases

- Codex's first run in an untrusted directory shows a "trust this directory?"
  prompt; mark the project trusted in `~/.codex/config.toml` first.
- Idle detection is heuristic (screen stability). Raise `stable_for` for slow /
  high-effort models. An unparseable turn is treated as UNKNOWN (never counts as
  clean), so a mis-timed cut costs an extra round, never a false convergence.
- Claude needs `--dangerously-skip-permissions` (default) so it doesn't stall on
  per-command approval prompts the dashboard can't answer.
