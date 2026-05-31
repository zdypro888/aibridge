package gitx

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDiffIncludesStagedFilesBeforeFirstCommit(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, repo, "init")

	if err := os.WriteFile(repo+"/staged.txt", []byte("review me\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	mustGit(t, repo, "add", "staged.txt")

	diff, err := Diff(repo)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "staged.txt") || !strings.Contains(diff, "review me") {
		t.Fatalf("Diff did not include staged unborn-repo content:\n%s", diff)
	}
}

func TestDiffIncludesUntrackedFileWithNewlineInName(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, repo, "init")

	name := "new\nfile.txt"
	if err := os.WriteFile(repo+"/"+name, []byte("review newline path\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	diff, err := Diff(repo)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "new\nfile.txt") || !strings.Contains(diff, "review newline path") {
		t.Fatalf("Diff did not include newline-path untracked content:\n%s", diff)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
