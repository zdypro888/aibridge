package promptlib

import (
	"path/filepath"
	"testing"
)

// TestDefaultAlwaysHasBuiltin verifies the built-in default template exists and
// is the active one out of the box.
func TestDefaultAlwaysHasBuiltin(t *testing.T) {
	lib := Default()
	if lib.Active != DefaultTemplateID {
		t.Fatalf("active should be %q, got %q", DefaultTemplateID, lib.Active)
	}
	if lib.Get(DefaultTemplateID) == nil {
		t.Fatal("default library must contain the built-in template")
	}
	if err := lib.Validate(); err != nil {
		t.Fatalf("default library should validate: %v", err)
	}
}

// TestSaveLoadRoundTrip checks a custom template survives a save+load.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.json")
	lib := Default()
	lib.Templates = append(lib.Templates, Template{
		ID:     "strict",
		Name:   "Strict",
		Codex:  SidePrompts{First: "codex first", Next: "codex next"},
		Claude: SidePrompts{First: "claude first"},
		Ask:    "anything else?",
	})
	lib.Active = "strict"

	if err := Save(path, lib); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Active != "strict" {
		t.Fatalf("active not preserved: %q", got.Active)
	}
	st := got.Get("strict")
	if st == nil || st.Codex.First != "codex first" || st.Ask != "anything else?" {
		t.Fatalf("custom template not preserved: %+v", st)
	}
	if got.Get(DefaultTemplateID) == nil {
		t.Fatal("default template must still exist after round-trip")
	}
}

// TestNormalizeReinsertsDefaultAndFixesActive ensures a library missing the
// built-in default (or pointing Active at a ghost) is repaired on load.
func TestNormalizeReinsertsDefaultAndFixesActive(t *testing.T) {
	lib := Library{Active: "ghost", Templates: []Template{{ID: "x", Name: "X"}}}
	lib.normalize()
	if lib.Get(DefaultTemplateID) == nil {
		t.Fatal("normalize must reinsert the built-in default")
	}
	if lib.Active != DefaultTemplateID {
		t.Fatalf("normalize must reset a dangling Active to default, got %q", lib.Active)
	}
}

// TestDefaultHasBothBuiltins verifies both built-in templates ship by default
// with the right review kinds.
func TestDefaultHasBothBuiltins(t *testing.T) {
	lib := Default()
	d := lib.Get(DefaultTemplateID)
	f := lib.Get(FullReviewTemplateID)
	if d == nil || f == nil {
		t.Fatalf("both built-ins must exist: diff=%v full=%v", d != nil, f != nil)
	}
	if d.ReviewKind() != KindDiff {
		t.Fatalf("default kind = %q, want %q", d.ReviewKind(), KindDiff)
	}
	if f.ReviewKind() != KindFull {
		t.Fatalf("full-review kind = %q, want %q", f.ReviewKind(), KindFull)
	}
	if !IsBuiltin(DefaultTemplateID) || !IsBuiltin(FullReviewTemplateID) {
		t.Fatal("both built-in ids must report IsBuiltin")
	}
	if IsBuiltin("custom") {
		t.Fatal("custom id must not report IsBuiltin")
	}
}

// TestReviewKindDefaultsToDiff covers the empty-Kind fallback.
func TestReviewKindDefaultsToDiff(t *testing.T) {
	if (Template{}).ReviewKind() != KindDiff {
		t.Fatalf("empty Kind should default to %q", KindDiff)
	}
}

// TestNormalizeReinsertsFullReview ensures a library that dropped the full-review
// built-in regains it on normalize.
func TestNormalizeReinsertsFullReview(t *testing.T) {
	lib := Library{Active: DefaultTemplateID, Templates: []Template{{ID: DefaultTemplateID, Name: "d", Kind: KindDiff}}}
	lib.normalize()
	if lib.Get(FullReviewTemplateID) == nil {
		t.Fatal("normalize must reinsert the full-review built-in")
	}
}

// TestValidateRejectsBad covers duplicate ids and an unresolvable active.
func TestValidateRejectsBad(t *testing.T) {
	dup := Library{
		Active:    DefaultTemplateID,
		Templates: []Template{{ID: DefaultTemplateID}, {ID: "a"}, {ID: "a"}},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("expected duplicate-id error")
	}
	noActive := Library{
		Active:    "missing",
		Templates: []Template{{ID: DefaultTemplateID}},
	}
	if err := noActive.Validate(); err == nil {
		t.Fatal("expected unresolvable-active error")
	}
}
