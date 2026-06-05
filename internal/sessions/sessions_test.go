package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeDirName(t *testing.T) {
	got := claudeDirName("/Users/zd/Developer/src/golang/aibridge")
	want := "-Users-zd-Developer-src-golang-aibridge"
	if got != want {
		t.Fatalf("claudeDirName = %q, want %q", got, want)
	}
	if g := claudeDirName("/private/tmp/aibridge-sandbox"); g != "-private-tmp-aibridge-sandbox" {
		t.Fatalf("hyphen path encoded wrong: %q", g)
	}
}

func TestListClaude_MatchesByRecordedCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/work/proj"

	// Put transcripts under ARBITRARY project-dir names (NOT the encoded cwd) to
	// prove matching is by the recorded cwd inside the file, not the dir name.
	mk := func(dirName, file, cwd string, mod time.Time) {
		d := filepath.Join(home, ".claude", "projects", dirName)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"type":"attachment","cwd":"` + cwd + `"}` + "\n" +
			`{"type":"user","message":{"role":"user","content":"hi there"}}` + "\n"
		p := filepath.Join(d, file)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	mk("zzz-weird-name", "aaaaaaaa-1111.jsonl", repo, time.Now().Add(-2*time.Hour))
	mk("another-dir", "bbbbbbbb-2222.jsonl", repo, time.Now())
	mk("other-proj", "cccccccc-3333.jsonl", "/work/elsewhere", time.Now()) // must not leak

	got, err := List("claude", repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions for %s, got %d: %+v", repo, len(got), got)
	}
	if got[0].ID != "bbbbbbbb-2222" {
		t.Fatalf("expected newest first, got %q", got[0].ID)
	}
}

func TestListCodex_FiltersByCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/work/proj"
	day := filepath.Join(home, ".codex", "sessions", "2026", "05", "31")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := `{"timestamp":"2026-05-31T10:00:00.000Z","type":"session_meta","payload":{"id":"019e-mine","timestamp":"2026-05-31T10:00:00.000Z","cwd":"/work/proj"}}` + "\n"
	other := `{"timestamp":"2026-05-31T11:00:00.000Z","type":"session_meta","payload":{"id":"019e-other","timestamp":"2026-05-31T11:00:00.000Z","cwd":"/work/elsewhere"}}` + "\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-a.jsonl"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-b.jsonl"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := List("codex", repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "019e-mine" {
		t.Fatalf("expected only the matching-cwd session, got %+v", got)
	}
}

func TestList_MissingStoreIsEmptyNotError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, side := range []string{"codex", "claude"} {
		got, err := List(side, "/whatever")
		if err != nil {
			t.Fatalf("%s: unexpected error for missing store: %v", side, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: expected empty, got %+v", side, got)
		}
	}
}
