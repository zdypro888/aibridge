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

func TestDiffDoesNotFollowUntrackedSymlink(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, repo, "init")

	// A secret file OUTSIDE the repo that an untracked symlink points at.
	outside := t.TempDir() + "/secret.txt"
	const secret = "EXTERNAL-SECRET-MUST-NOT-LEAK"
	if err := os.WriteFile(outside, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	if err := os.Symlink(outside, repo+"/link"); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	diff, err := Diff(repo)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// The link's target content must never appear (no os.ReadFile through it),
	// but the link path + target string should, so a new symlink still moves the
	// convergence hash.
	if strings.Contains(diff, secret) {
		t.Fatalf("Diff leaked external file content through a symlink:\n%s", diff)
	}
	if !strings.Contains(diff, "link") || !strings.Contains(diff, outside) {
		t.Fatalf("Diff did not record the untracked symlink path/target:\n%s", diff)
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
