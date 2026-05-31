package bridge

import (
	"strings"
	"testing"
)

func TestParseVerdict_TakesLastNotInstruction(t *testing.T) {
	// Simulates a real TUI screen: our prompt (which lists all three verdicts as
	// instructions) is echoed, then the agent emits its actual verdict last.
	screen := `> Review the changes.
AUDIT_RESULT: CLEAN means no problems
AUDIT_RESULT: FIXED means you edited
AUDIT_RESULT: ISSUES means you found but did not fix

• I reviewed the diff and found a nil deref; I fixed it.
AUDIT_RESULT: FIXED`

	if got := parseVerdict(screen); got != VerdictFixed {
		t.Fatalf("expected FIXED (last real verdict), got %q", got)
	}
}

func TestParseVerdict_None(t *testing.T) {
	if got := parseVerdict("no verdict here"); got != VerdictUnknown {
		t.Fatalf("expected UNKNOWN, got %q", got)
	}
}

func TestParseVerdict_Clean(t *testing.T) {
	if got := parseVerdict("looks good\nAUDIT_RESULT: CLEAN\n"); got != VerdictClean {
		t.Fatalf("expected CLEAN, got %q", got)
	}
}

func TestParseNoMoreBugs(t *testing.T) {
	cases := map[string]bool{
		"all good\nAUDIT_RESULT: CLEAN\nNO_MORE_BUGS": true,
		"AUDIT_RESULT: ISSUES\nMORE_BUGS":             false,
		"nothing here":                                false,
		// The echoed prompt contains both tokens before the verdict; only the
		// region AFTER the last AUDIT_RESULT counts, and there it's NO_MORE_BUGS.
		"...NO_MORE_BUGS or MORE_BUGS...\nAUDIT_RESULT: CLEAN\nNO_MORE_BUGS": true,
		// A real MORE_BUGS after the verdict means not done.
		"AUDIT_RESULT: ISSUES\nstill MORE_BUGS to fix": false,
	}
	for screen, want := range cases {
		if got := parseNoMoreBugs(screen); got != want {
			t.Errorf("parseNoMoreBugs(%q)=%v want %v", screen, got, want)
		}
	}
}

func TestPrompts_RenderSingleLineAndIncludeAsk(t *testing.T) {
	ps, err := NewPromptSet("codex", "", "", "en")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, tc := range []struct {
		handoff string
		ask     bool
	}{
		{"", false},
		{"prev was claude; verdict CLEAN", true},
		{"multi\nline\nhandoff", true},
	} {
		p := ps.Render(tc.handoff, tc.ask)
		if strings.Contains(p, "\n") {
			t.Errorf("rendered prompt has newline (handoff=%q): %q", tc.handoff, p)
		}
		if !strings.Contains(p, "AUDIT_RESULT") {
			t.Errorf("prompt missing verdict instruction")
		}
		if tc.ask && !strings.Contains(p, "NO_MORE_BUGS") {
			t.Errorf("ask=true but prompt missing ask block")
		}
		if !tc.ask && strings.Contains(p, "NO_MORE_BUGS") {
			t.Errorf("ask=false but prompt has ask block")
		}
	}
}

func TestPrompts_Chinese(t *testing.T) {
	ps, err := NewPromptSet("claude", "", "", "zh")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p := ps.Render("", true)
	if strings.Contains(p, "\n") {
		t.Errorf("zh prompt should be single line: %q", p)
	}
	// Machine tokens stay ASCII even in Chinese mode.
	if !strings.Contains(p, "AUDIT_RESULT") || !strings.Contains(p, "NO_MORE_BUGS") {
		t.Errorf("zh prompt must keep ASCII tokens: %q", p)
	}
	// Human text + reply directive are Chinese.
	if !strings.Contains(p, "审查") || !strings.Contains(p, "中文") {
		t.Errorf("zh prompt should contain Chinese instructions: %q", p)
	}
}

func TestPrompts_CustomTemplate(t *testing.T) {
	ps, err := NewPromptSet("codex", "CUSTOM first {{.Verdict}}", "CUSTOM next {{.Handoff}} {{.Verdict}}", "en")
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

func TestPrompts_CustomAskPrompt(t *testing.T) {
	ps, err := NewPromptSet("codex", "", "", "en", "Double-check concurrency edge cases.")
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

func TestPrompts_BadTemplateErrors(t *testing.T) {
	if _, err := NewPromptSet("codex", "{{.Unclosed", "", "en"); err == nil {
		t.Fatalf("expected error for malformed template")
	}
}
