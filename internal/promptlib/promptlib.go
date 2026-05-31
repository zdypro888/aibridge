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
	Codex  SidePrompts `json:"codex"`  // codex's first/next (empty = built-in default)
	Claude SidePrompts `json:"claude"` // claude's first/next (empty = built-in default)
	Ask    string      `json:"ask"`    // optional custom ask-gate question (empty = default)
}

// Library is the whole template collection plus which one is active.
type Library struct {
	Active    string     `json:"active"`    // id of the selected template
	Templates []Template `json:"templates"` // all templates; always includes "default"
}

// DefaultTemplateID is the built-in, undeletable template (all-empty = use the
// built-in go-audit defaults for both sides).
const DefaultTemplateID = "default"

// Default returns a library containing only the built-in default template.
func Default() Library {
	return Library{
		Active: DefaultTemplateID,
		Templates: []Template{
			{ID: DefaultTemplateID, Name: "默认（go-audit 严格审查）"},
		},
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

// normalize guarantees the built-in default template exists and that Active
// points at a real template.
func (l *Library) normalize() {
	hasDefault := false
	for _, t := range l.Templates {
		if t.ID == DefaultTemplateID {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		// Prepend the immutable built-in default.
		l.Templates = append([]Template{{ID: DefaultTemplateID, Name: "默认（go-audit 严格审查）"}}, l.Templates...)
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
	return Template{ID: DefaultTemplateID, Name: "默认（go-audit 严格审查）"}
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
