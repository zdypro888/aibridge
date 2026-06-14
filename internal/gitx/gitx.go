// Package gitx wraps the git operations the bridge needs: capturing the working
// tree's pending changes and hashing them. The diff is the communication channel
// between the two agents, and a stable diff hash across a full round is the
// fixpoint signal that both sides have stopped changing the code.
package gitx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	out, err := run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// Diff returns the full pending change of the work tree relative to HEAD,
// including the content of untracked files. This is what one agent hands to the
// other to review.
func Diff(dir string) (string, error) {
	tracked, err := run(dir, "diff", "HEAD")
	if err != nil {
		// A repo with no commits has no HEAD. In that state, staged files are
		// visible only through --cached; plain `git diff` shows unstaged edits.
		cached, cerr := run(dir, "diff", "--cached")
		if cerr != nil {
			return "", cerr
		}
		unstaged, uerr := run(dir, "diff")
		if uerr != nil {
			return "", uerr
		}
		tracked = cached + unstaged
	}

	untracked, err := untrackedContent(dir)
	if err != nil {
		return "", err
	}
	return tracked + untracked, nil
}

// Hash returns a stable fingerprint of the current pending changes. Two calls
// returning the same hash means no code changed in between.
func Hash(dir string) (string, error) {
	d, err := Diff(dir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(d))
	return hex.EncodeToString(sum[:]), nil
}

// untrackedContent lists untracked files and appends their content so brand-new
// files also move the hash. Without this, an agent creating a new file would
// look like "no change" since `git diff` ignores untracked paths.
func untrackedContent(dir string) (string, error) {
	out, err := run(dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	// One NUL-delimited filename per entry. Splitting on whitespace or newlines
	// would break valid paths and silently drop content from the hash, breaking
	// the diff-fixpoint signal.
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	sort.Strings(files)

	var b strings.Builder
	for _, f := range files {
		full := filepath.Join(dir, f)
		// Lstat (not Stat) so a symlink is seen as a symlink, not its target.
		fi, lerr := os.Lstat(full)
		if lerr != nil {
			continue
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			// Record the link target string — exactly what git stores for a
			// symlink — instead of os.ReadFile'ing through it. Following the link
			// would splice an arbitrary external file's content (e.g. a symlink to
			// /etc/passwd or a file outside the repo) into the diff that is handed
			// to the other agent and rendered in the web UI, and would make the
			// convergence hash drift whenever that external target changes.
			target, terr := os.Readlink(full)
			if terr != nil {
				continue
			}
			fmt.Fprintf(&b, "\n+++ untracked %s -> %s\n", f, target)
		case fi.Mode().IsRegular():
			raw, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			fmt.Fprintf(&b, "\n+++ untracked %s\n%s", f, raw)
		default:
			// Fifos/devices/sockets aren't content git would track (git omits them
			// from ls-files), but skip defensively: reading one can block forever.
		}
	}
	return b.String(), nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
