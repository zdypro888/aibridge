// Package config defines the full, user-editable configuration for a bridge run:
// per-agent settings (launch command, prompt templates, completion detection,
// enabled flag) and flow settings (who goes first, round cap, which convergence
// strategy, confirmation gate). It loads from YAML with sensible defaults so a
// zero-config run still works, and validates so the UI can surface errors.
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole bridge configuration. Everything here is editable from the
// config file and the web UI.
// JSON tags mirror the YAML names so the web UI (which uses lowercase keys) reads
// and writes the same field names the config file uses.
type Config struct {
	Repo   string       `yaml:"repo" json:"repo"`     // shared git work tree both agents operate on
	Lang   string       `yaml:"lang" json:"lang"`     // language for built-in prompts AND the agents' reports/replies: "en" | "zh"
	Agents AgentConfig  `yaml:"agents" json:"agents"` // per-side settings
	Flow   FlowConfig   `yaml:"flow" json:"flow"`     // orchestration settings
	Server ServerConfig `yaml:"server" json:"server"` // web dashboard settings
}

// AgentConfig holds both sides. They are symmetric in shape but configured
// independently so you can, e.g., disable one side or give each a different model.
type AgentConfig struct {
	Codex  Agent `yaml:"codex" json:"codex"`
	Claude Agent `yaml:"claude" json:"claude"`
}

// Agent is one reviewer's full configuration.
// Agent holds one reviewer's runtime settings. Prompt templates live separately
// in the prompt library (see internal/promptlib) and are NOT part of the config.
type Agent struct {
	Enabled bool   `yaml:"enabled" json:"enabled"` // if false, this side is skipped entirely (single-AI mode)
	Command string `yaml:"command" json:"command"` // shell command to launch the interactive TUI on a PTY

	// Completion detection (screen-stability heuristic) for this side. Real
	// high-effort models need a longer StableFor than fast ones.
	Poll      Duration `yaml:"poll" json:"poll"`             // screen sample interval
	StableFor Duration `yaml:"stable_for" json:"stable_for"` // unchanged-screen duration that counts as "done"
	SettleFor Duration `yaml:"settle_for" json:"settle_for"` // max wait for the response to START rendering
	Timeout   Duration `yaml:"timeout" json:"timeout"`       // hard per-turn ceiling
	// BusyPattern is a regexp matching the TUI's "working" status line (e.g. "esc
	// to interrupt"). While the screen matches it, the turn is held as still
	// in-progress regardless of stability, so a thinking/streaming agent that
	// pauses with a static screen isn't mistaken for finished. Empty = built-in
	// default (see DefaultBusyPattern); never matching is fine — Timeout backstops.
	BusyPattern string `yaml:"busy_pattern" json:"busy_pattern"`
}

// DefaultBusyPattern matches both codex's and claude's working status line. Both
// TUIs render "esc to interrupt" while a turn is active and drop it when idle.
// Case-insensitive because codex sometimes capitalizes "Esc".
const DefaultBusyPattern = `(?i)esc to interrupt`

// FlowConfig controls the ping-pong loop and convergence.
type FlowConfig struct {
	First     string `yaml:"first" json:"first"`           // "codex" or "claude": who reviews first
	MaxRounds int    `yaml:"max_rounds" json:"max_rounds"` // hard cap on turns; 0 = unlimited (run until convergence or stop)
	// Strategy selects how convergence ("both clean, no more bugs") is decided.
	// Pluggable so the flow isn't hard-wired: "diff-fixpoint" | "ask-gate" |
	// "combined". See bridge/strategy.go.
	Strategy string `yaml:"strategy" json:"strategy"`
}

// ServerConfig controls the web dashboard.
type ServerConfig struct {
	Addr string `yaml:"addr" json:"addr"` // listen address, e.g. "127.0.0.1:8799"
}

// Default returns a fully-populated config that works with no file present.
func Default() Config {
	return Config{
		Repo: ".",
		Lang: "zh",
		Agents: AgentConfig{
			Codex: Agent{
				Enabled:     true,
				Command:     "/Applications/Codex.app/Contents/Resources/codex",
				Poll:        Duration(500 * time.Millisecond),
				StableFor:   Duration(10 * time.Second),
				SettleFor:   Duration(30 * time.Second),
				Timeout:     Duration(10 * time.Minute),
				BusyPattern: DefaultBusyPattern,
			},
			Claude: Agent{
				Enabled:     true,
				Command:     "claude --dangerously-skip-permissions",
				Poll:        Duration(500 * time.Millisecond),
				StableFor:   Duration(10 * time.Second),
				SettleFor:   Duration(30 * time.Second),
				Timeout:     Duration(10 * time.Minute),
				BusyPattern: DefaultBusyPattern,
			},
		},
		Flow: FlowConfig{
			First:     "codex",
			MaxRounds: 8,
			Strategy:  "combined",
		},
		Server: ServerConfig{Addr: "127.0.0.1:8799"},
	}
}

// Load reads YAML at path and overlays it on the defaults. A missing file is not
// an error — defaults are returned — so first-run is zero-config.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config back to path as YAML (used when the UI edits it).
func Save(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Validate checks the config is runnable and returns a descriptive error if not.
func (c Config) Validate() error {
	if c.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if !c.Agents.Codex.Enabled && !c.Agents.Claude.Enabled {
		return fmt.Errorf("at least one agent must be enabled")
	}
	switch c.Flow.First {
	case "codex", "claude":
	default:
		return fmt.Errorf("flow.first must be codex or claude, got %q", c.Flow.First)
	}
	switch c.Flow.Strategy {
	case "diff-fixpoint", "ask-gate", "combined":
	default:
		return fmt.Errorf("flow.strategy must be diff-fixpoint|ask-gate|combined, got %q", c.Flow.Strategy)
	}
	switch c.Lang {
	case "", "en", "zh":
	default:
		return fmt.Errorf("lang must be en or zh, got %q", c.Lang)
	}
	// MaxRounds <= 0 is allowed and means "unlimited": run until the agents
	// converge (both clean, no more bugs) or the user stops the run — needed by
	// the whole-codebase review, which must not be cut off by a round cap.
	if err := validateAgent("codex", c.Agents.Codex); err != nil {
		return err
	}
	if err := validateAgent("claude", c.Agents.Claude); err != nil {
		return err
	}
	return nil
}

func validateAgent(name string, a Agent) error {
	if !a.Enabled {
		return nil
	}
	if a.Command == "" {
		return fmt.Errorf("agents.%s.command is required", name)
	}
	for field, value := range map[string]Duration{
		"poll":       a.Poll,
		"stable_for": a.StableFor,
		"settle_for": a.SettleFor,
		"timeout":    a.Timeout,
	} {
		if value <= 0 {
			return fmt.Errorf("agents.%s.%s must be positive", name, field)
		}
	}
	if a.BusyPattern != "" {
		if _, err := regexp.Compile(a.BusyPattern); err != nil {
			return fmt.Errorf("agents.%s.busy_pattern is not a valid regexp: %w", name, err)
		}
	}
	return nil
}
