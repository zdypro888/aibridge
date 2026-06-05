// Package sessions enumerates the prior interactive sessions of the underlying
// coding-agent CLIs (codex and claude) for a given repository, so the dashboard
// can offer "continue a previous session" with a concrete picker.
//
// Each CLI stores its own transcripts on disk; this package only READS them:
//
//   - codex: ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl, whose first
//     line is a session_meta record carrying the session id and the cwd it ran
//     in. We filter by cwd so only this repo's sessions are offered.
//   - claude: ~/.claude/projects/<encoded-cwd>/<uuid>.jsonl, where the directory
//     name is the cwd with every non-alphanumeric byte replaced by '-', and the
//     file name (sans .jsonl) is the session id. We use the file mtime as the
//     session time.
//
// Resume itself is performed by the runner, which appends the right flag to the
// agent command (claude: --resume <id>/--continue; codex: resume <id>/--last).
package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Session is one resumable prior conversation for an agent.
type Session struct {
	ID    string `json:"id"`    // session id passed back to resume
	Time  string `json:"time"`  // RFC3339-ish timestamp, for display + sorting
	Label string `json:"label"` // human label shown in the picker
}

// maxSessions caps how many sessions are returned (most-recent first) so the
// picker stays usable and scanning stays bounded.
const maxSessions = 50

// List returns this repo's resumable sessions for the given side ("codex" or
// "claude"), most-recent first. A missing store yields an empty list, not an
// error, so the UI degrades gracefully on a machine that has never run the CLI.
func List(side, repoDir string) ([]Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		abs = repoDir
	}
	abs = filepath.Clean(abs)
	// Also compute the symlink-resolved form so a cwd recorded as e.g.
	// /private/tmp/x still matches a configured /tmp/x (macOS) and vice versa.
	absReal := abs
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		absReal = filepath.Clean(r)
	}

	switch side {
	case "codex":
		return listCodex(filepath.Join(home, ".codex", "sessions"), abs, absReal)
	case "claude":
		return listClaude(filepath.Join(home, ".claude", "projects"), abs)
	default:
		return nil, fmt.Errorf("unknown side %q", side)
	}
}

// listClaude reads ~/.claude/projects/<encoded-cwd>/*.jsonl. The id is the file
// name; the time is the file mtime.
func listClaude(projectsDir, repoDir string) ([]Session, error) {
	dir := filepath.Join(projectsDir, claudeDirName(repoDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		t := info.ModTime().UTC().Format("2006-01-02 15:04")
		out = append(out, Session{ID: id, Time: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"), Label: sessionLabel(t, firstUserMessageClaude(filepath.Join(dir, e.Name())), id)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return cap50(out), nil
}

// codexMeta is the minimal shape of a codex session_meta first line.
type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		Cwd       string `json:"cwd"`
	} `json:"payload"`
}

// listCodex walks ~/.codex/sessions, reads each rollout's first line, and keeps
// the ones whose recorded cwd matches repoDir. Files are visited mtime-desc so
// the most relevant sessions are found first.
func listCodex(root, repoDir, repoReal string) ([]Session, error) {
	type fileMeta struct {
		path  string
		mtime int64
	}
	var files []fileMeta
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than aborting the walk
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		files = append(files, fileMeta{path: path, mtime: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })

	var out []Session
	for _, f := range files {
		if len(out) >= maxSessions {
			break
		}
		meta, ok := readCodexMeta(f.path)
		if !ok || meta.Type != "session_meta" {
			continue
		}
		cwd := filepath.Clean(meta.Payload.Cwd)
		if cwd != repoDir && cwd != repoReal {
			continue
		}
		ts := meta.Payload.Timestamp
		when := ts
		if len(ts) >= 16 {
			when = strings.Replace(ts[:16], "T", " ", 1)
		}
		out = append(out, Session{ID: meta.Payload.ID, Time: ts, Label: sessionLabel(when, firstUserMessageCodex(f.path), meta.Payload.ID)})
	}
	return out, nil
}

// readCodexMeta reads and parses only the first line of a codex rollout file.
func readCodexMeta(path string) (codexMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexMeta{}, false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return codexMeta{}, false
	}
	var m codexMeta
	if json.Unmarshal([]byte(line), &m) != nil {
		return codexMeta{}, false
	}
	return m, true
}

// claudeDirName encodes a cwd the way Claude Code names its project directory:
// every non-alphanumeric byte becomes '-'.
func claudeDirName(repoDir string) string {
	var b strings.Builder
	for _, r := range repoDir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// sessionLabel builds the picker label: "<when> · <summary>", falling back to the
// short id when no summary could be extracted, so an entry is always recognizable.
func sessionLabel(when, summary, id string) string {
	summary = cleanSummary(summary)
	if summary == "" {
		return when + "  " + shortID(id)
	}
	return when + "  " + summary
}

// summaryMaxRunes bounds the summary length shown in the picker.
const summaryMaxRunes = 60

// firstScanLines caps how many leading lines we read looking for the first user
// message (transcripts can start with system/queue/attachment records).
const firstScanLines = 40

// cleanSummary flattens whitespace and truncates to summaryMaxRunes runes.
func cleanSummary(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	r := []rune(s)
	if len(r) > summaryMaxRunes {
		return string(r[:summaryMaxRunes]) + "…"
	}
	return s
}

// looksLikeSystemSeed reports whether a first message is an automated/system seed
// (e.g. memory-agent boot, our own injected review prompt) rather than something
// the user typed — so we skip it and keep looking for a human-meaningful line.
func looksLikeSystemSeed(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{
		"hello memory agent",
		"you are one of two ai",        // our review prompts (EN)
		"你是两个 ai",                      // our review prompts (ZH)
		"<system-reminder>", "caveat:", // tooling preambles
	} {
		if strings.HasPrefix(t, p) || strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// firstUserMessageClaude scans the start of a claude transcript for the first
// human user message text. Best-effort: returns "" if none found.
func firstUserMessageClaude(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; i < firstScanLines && sc.Scan(); i++ {
		var d struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Type != "user" {
			continue
		}
		txt := claudeContentText(d.Message.Content)
		if txt == "" || looksLikeSystemSeed(txt) {
			continue
		}
		return txt
	}
	return ""
}

// claudeContentText extracts text from a claude message.content that may be a
// plain string or an array of content blocks.
func claudeContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return b.Text
			}
		}
	}
	return ""
}

// firstUserMessageCodex scans the start of a codex rollout for the first user
// message text (event_msg / user_message). Best-effort: returns "" if none.
func firstUserMessageCodex(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; i < firstScanLines && sc.Scan(); i++ {
		var d struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		if d.Type == "event_msg" && d.Payload.Type == "user_message" {
			txt := d.Payload.Message
			if txt == "" || looksLikeSystemSeed(txt) {
				continue
			}
			return txt
		}
	}
	return ""
}

// shortID returns a compact form of a session id for display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func cap50(s []Session) []Session {
	if len(s) > maxSessions {
		return s[:maxSessions]
	}
	return s
}
