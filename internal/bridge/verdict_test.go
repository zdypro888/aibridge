package bridge

import (
	"strings"
	"testing"
)

// TestParseVerdict checks the parser is line-anchored and takes the LAST
// AUDIT_RESULT (the submitted prompt is echoed on screen and also contains the
// token, so an early match must not win).
func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   Verdict
	}{
		{"none", "just some output\nno verdict here", VerdictUnknown},
		{"clean", "review done\nAUDIT_RESULT: CLEAN", VerdictClean},
		{"fixed", "AUDIT_RESULT: FIXED\n", VerdictFixed},
		{"issues", "AUDIT_RESULT: ISSUES", VerdictIssues},
		{"last wins over echoed prompt", "finish with AUDIT_RESULT: CLEAN if...\n...\nAUDIT_RESULT: FIXED", VerdictFixed},
		{"prose mention not anchored", "I will write AUDIT_RESULT: CLEAN at the end", VerdictUnknown},
		{"leading whitespace ok", "   AUDIT_RESULT: CLEAN", VerdictClean},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseVerdict(c.screen); got != c.want {
				t.Fatalf("parseVerdict(%q) = %v, want %v", c.screen, got, c.want)
			}
		})
	}
}

// TestParseNoMoreBugs checks the ask-gate confirmation: only the region after the
// last verdict counts; NO_MORE_BUGS means done, a bare MORE_BUGS means not done.
func TestParseNoMoreBugs(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"no tokens", "AUDIT_RESULT: CLEAN", false},
		{"no more bugs", "AUDIT_RESULT: CLEAN\nNO_MORE_BUGS", true},
		{"more bugs", "AUDIT_RESULT: ISSUES\nMORE_BUGS", false},
		{"more bugs after verdict beats echoed no_more", "write NO_MORE_BUGS or MORE_BUGS\nAUDIT_RESULT: ISSUES\nMORE_BUGS", false},
		{"no_more after verdict, echoed tokens before ignored", "...MORE_BUGS...\nAUDIT_RESULT: CLEAN\nNO_MORE_BUGS", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseNoMoreBugs(c.screen); got != c.want {
				t.Fatalf("parseNoMoreBugs(%q) = %v, want %v", c.screen, got, c.want)
			}
		})
	}
}

// TestRenderForcesVerdict proves Render appends the machine verdict instruction
// even when a custom template omits it, so a user can't break convergence by
// editing the prompt. Also checks single-line output and that the ask-block is
// added only when asking.
func TestRenderForcesVerdict(t *testing.T) {
	ps, err := NewPromptSet(KindDiff, "codex", "just look at the code", "look again", "en")
	if err != nil {
		t.Fatalf("NewPromptSet: %v", err)
	}

	first := ps.Render("", false)
	if !strings.Contains(first, "AUDIT_RESULT") {
		t.Fatalf("Render must force AUDIT_RESULT onto a custom prompt that omits it; got %q", first)
	}
	if strings.Contains(first, "\n") {
		t.Fatalf("rendered prompt must be single-line; got %q", first)
	}

	if asked := ps.Render("prev: clean", true); !strings.Contains(asked, "NO_MORE_BUGS") {
		t.Fatalf("Render(ask=true) must include NO_MORE_BUGS; got %q", asked)
	}
	if strings.Contains(ps.Render("prev: clean", false), "NO_MORE_BUGS") {
		t.Fatalf("Render(ask=false) must not include NO_MORE_BUGS")
	}
}

// TestRenderDefaultNoDuplicateVerdict makes sure a well-formed default template
// (which already ends with the verdict instruction) is NOT given a second copy
// by the force-append. We can't count the token "AUDIT_RESULT" because the human
// text legitimately mentions it twice (the verdict line and "on the line after
// AUDIT_RESULT"); instead count a phrase unique to the verdict instruction.
func TestRenderDefaultNoDuplicateVerdict(t *testing.T) {
	const verdictMarker = "加冒号再加一个英文单词"      // only in verdictInstruction(zh)
	const askMarker = "写 token NO_MORE_BUGS" // only in askInstruction(zh)

	ps, err := NewPromptSet(KindDiff, "claude", "", "", "zh")
	if err != nil {
		t.Fatalf("NewPromptSet: %v", err)
	}
	out := ps.Render("", true)
	if n := strings.Count(out, verdictMarker); n != 1 {
		t.Fatalf("verdict instruction should appear exactly once, got %d: %q", n, out)
	}
	if n := strings.Count(out, askMarker); n != 1 {
		t.Fatalf("ask instruction should appear exactly once, got %d: %q", n, out)
	}
}

// TestPrompts_CustomTemplate confirms custom first/next templates are used.
func TestPrompts_CustomTemplate(t *testing.T) {
	ps, err := NewPromptSet(KindDiff, "codex", "CUSTOM first {{.Verdict}}", "CUSTOM next {{.Handoff}} {{.Verdict}}", "en")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if p := ps.Render("", false); !strings.HasPrefix(p, "CUSTOM first") {
		t.Errorf("custom first template not used: %q", p)
	}
	if p := ps.Render("HANDOFF", false); !strings.Contains(p, "CUSTOM next HANDOFF") {
		t.Errorf("custom next template not used: %q", p)
	}
}

// TestPrompts_CustomAskPrompt confirms a user-supplied ask prompt is rendered
// while the machine NO_MORE_BUGS/MORE_BUGS instruction is preserved.
func TestPrompts_CustomAskPrompt(t *testing.T) {
	ps, err := NewPromptSet(KindDiff, "codex", "", "", "en", "Double-check concurrency edge cases.")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p := ps.Render("", true)
	if !strings.Contains(p, "Double-check concurrency edge cases.") {
		t.Fatalf("custom ask prompt not rendered: %q", p)
	}
	if !strings.Contains(p, "NO_MORE_BUGS") || !strings.Contains(p, "MORE_BUGS") {
		t.Fatalf("custom ask prompt must preserve machine-token instruction: %q", p)
	}
}

// TestPrompts_BadTemplateErrors confirms malformed templates surface an error.
func TestPrompts_BadTemplateErrors(t *testing.T) {
	if _, err := NewPromptSet(KindDiff, "codex", "{{.Unclosed", "", "en"); err == nil {
		t.Fatalf("expected error for malformed template")
	}
}
