package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionLabel_FallsBackToID(t *testing.T) {
	got := sessionLabel("2026-06-04 19:20", "", "abcdef1234")
	if got != "2026-06-04 19:20  abcdef12" {
		t.Fatalf("expected id fallback, got %q", got)
	}
}

func TestSessionLabel_UsesSummary(t *testing.T) {
	got := sessionLabel("2026-06-04 19:20", "  审查  calc.go  的 bug\n并修复  ", "id")
	if got != "2026-06-04 19:20  审查 calc.go 的 bug 并修复" {
		t.Fatalf("summary not cleaned/used: %q", got)
	}
}

func TestCleanSummary_Truncates(t *testing.T) {
	long := strings.Repeat("x", 100)
	out := cleanSummary(long)
	if r := []rune(out); len(r) != summaryMaxRunes+1 || string(r[len(r)-1]) != "…" {
		t.Fatalf("expected truncation to %d + ellipsis, got %d runes", summaryMaxRunes, len([]rune(out)))
	}
}

func TestFirstUserMessageClaude_SkipsSystemSeed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	// First user line is a system seed (skip), second is the real one.
	lines := `{"type":"queue-operation"}
{"type":"user","message":{"role":"user","content":"Hello memory agent, observe."}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"修复启动时的 nil panic"}]}}
`
	os.WriteFile(p, []byte(lines), 0o644)
	if got := firstUserMessageClaude(p); got != "修复启动时的 nil panic" {
		t.Fatalf("got %q", got)
	}
}

func TestFirstUserMessageCodex_FindsUserMessage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.jsonl")
	lines := `{"type":"session_meta","payload":{"id":"x","cwd":"/w"}}
{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"event_msg","payload":{"type":"user_message","message":"全量分析下当前项目"}}
`
	os.WriteFile(p, []byte(lines), 0o644)
	if got := firstUserMessageCodex(p); got != "全量分析下当前项目" {
		t.Fatalf("got %q", got)
	}
}
