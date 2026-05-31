// Package promptlib is the prompt-template library: a set of named templates,
// each holding both agents' first/next prompts plus the ask-gate question,
// persisted to its own JSON file (separate from the run config). The config only
// references a template by id; this package resolves it.
//
// A template field left empty means "use the built-in default for that side"
// (see bridge.DefaultPrompts), so a template can override just the pieces it
// cares about. The "default" template (all-empty) always exists and can't be
// deleted — it is the built-in go-audit doctrine.
package promptlib

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SidePrompts holds one agent's two prompt templates.
type SidePrompts struct {
	First string `json:"first"`
	Next  string `json:"next"`
}

// Template is one named, switchable prompt set covering both agents.
type Template struct {
	ID     string      `json:"id"`     // stable identifier referenced by config
	Name   string      `json:"name"`   // human label shown in the UI
	Kind   string      `json:"kind"`   // review doctrine for empty fields: "diff" or "full"
	Codex  SidePrompts `json:"codex"`  // codex's first/next (empty = built-in default)
	Claude SidePrompts `json:"claude"` // claude's first/next (empty = built-in default)
	Ask    string      `json:"ask"`    // optional custom ask-gate question (empty = default)
}

// ReviewKind returns the template's review doctrine, defaulting to "diff".
func (t Template) ReviewKind() string {
	if strings.TrimSpace(t.Kind) == "" {
		return KindDiff
	}
	return t.Kind
}

// Library is the whole template collection plus which one is active.
type Library struct {
	Active    string     `json:"active"`    // id of the selected template
	Templates []Template `json:"templates"` // all templates; always includes "default"
}

// Built-in template ids and review kinds. The built-in templates always exist
// (normalize re-inserts any that are missing) and are shown read-only in the UI;
// their empty fields resolve to the matching go-audit doctrine for the configured
// language.
const (
	DefaultTemplateID    = "default"     // 修改审核: review the pending diff
	FullReviewTemplateID = "full-review" // 代码全局审核: sweep the whole codebase

	KindDiff = "diff"
	KindFull = "full"
)

// builtinTemplates returns the always-present built-in templates, in display
// order. They carry empty prompt fields so each resolves to the built-in
// doctrine for its Kind + the configured language.
func builtinTemplates() []Template {
	return []Template{
		{ID: DefaultTemplateID, Name: "修改审核（go-audit 严格审查）", Kind: KindDiff},
		{ID: FullReviewTemplateID, Name: "代码全局审核（go-audit 全量审查）", Kind: KindFull},
	}
}

// IsBuiltin reports whether id names a built-in (undeletable, read-only) template.
func IsBuiltin(id string) bool {
	for _, b := range builtinTemplates() {
		if b.ID == id {
			return true
		}
	}
	return false
}

// Default returns a library containing the built-in templates, with the diff
// review active.
func Default() Library {
	return Library{
		Active:    DefaultTemplateID,
		Templates: builtinTemplates(),
	}
}

// Load reads the library JSON at path, overlaying onto Default so a missing file
// or a file lacking the default template still yields a usable library.
func Load(path string) (Library, error) {
	lib := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lib, nil
		}
		return lib, fmt.Errorf("read prompts %s: %w", path, err)
	}
	var loaded Library
	if err := json.Unmarshal(data, &loaded); err != nil {
		return lib, fmt.Errorf("parse prompts %s: %w", path, err)
	}
	lib = loaded
	lib.normalize()
	return lib, nil
}

// Save writes the library JSON to path (pretty-printed).
func Save(path string, lib Library) error {
	lib.normalize()
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// normalize guarantees every built-in template exists, that each template has a
// Kind, and that Active points at a real template. Missing built-ins are
// prepended in declared order so the defaults always lead the list.
func (l *Library) normalize() {
	var missing []Template
	for _, b := range builtinTemplates() {
		if l.Get(b.ID) == nil {
			missing = append(missing, b)
		}
	}
	if len(missing) > 0 {
		l.Templates = append(missing, l.Templates...)
	}
	for i := range l.Templates {
		if strings.TrimSpace(l.Templates[i].Kind) == "" {
			l.Templates[i].Kind = KindDiff
		}
	}
	if l.Get(l.Active) == nil {
		l.Active = DefaultTemplateID
	}
}

// Get returns the template with the given id, or nil.
func (l *Library) Get(id string) *Template {
	for i := range l.Templates {
		if l.Templates[i].ID == id {
			return &l.Templates[i]
		}
	}
	return nil
}

// ActiveTemplate returns the selected template (falling back to default).
func (l *Library) ActiveTemplate() Template {
	if t := l.Get(l.Active); t != nil {
		return *t
	}
	return builtinTemplates()[0]
}

// Validate checks the library is coherent (unique non-empty ids, default present,
// active resolvable). Returns a descriptive error for the UI.
func (l Library) Validate() error {
	seen := map[string]bool{}
	hasDefault := false
	for _, t := range l.Templates {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return fmt.Errorf("template id is required (name %q)", t.Name)
		}
		if seen[id] {
			return fmt.Errorf("duplicate template id %q", id)
		}
		seen[id] = true
		if id == DefaultTemplateID {
			hasDefault = true
		}
	}
	if !hasDefault {
		return fmt.Errorf("the built-in %q template must exist", DefaultTemplateID)
	}
	if !seen[strings.TrimSpace(l.Active)] {
		return fmt.Errorf("active template %q does not exist", l.Active)
	}
	return nil
}
