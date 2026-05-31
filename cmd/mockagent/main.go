// Command mockagent is a fake interactive coding agent used to end-to-end test
// the bridge's PTY drive + idle detection + convergence without burning real
// codex/claude turns. It behaves like a real TUI: prints a prompt, blocks on a
// submitted line of stdin, "thinks" (sleeps so the screen streams), optionally
// edits a file, prints a report ending with AUDIT_RESULT (and optionally
// NO_MORE_BUGS), then loops.
//
// Like a real coding-agent TUI it puts its pty into raw mode and frames input on
// CR/LF itself. This matters: in cooked mode the kernel caps each input line at
// MAX_CANON (~1024 bytes on macOS) and silently drops everything past it —
// including the terminating CR — so a long prompt (e.g. the go-audit doctrine)
// is never delivered as a complete line and the agent appears to hang forever.
// Raw mode removes that limit, exactly as the real codex/claude TUIs do.
//
// Env:
//
//	MOCK_NAME      label printed in output
//	MOCK_FIX_FILE  if set, the file this agent edits on a "FIXED" turn
//	MOCK_SCRIPT    comma-separated verdicts per turn, e.g. "FIXED,CLEAN"; after
//	               the script is exhausted it returns CLEAN forever
//	MOCK_NOMORE    if set, append NO_MORE_BUGS on CLEAN turns (for ask/combined)
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func main() {
	name := envOr("MOCK_NAME", "mock")
	fixFile := os.Getenv("MOCK_FIX_FILE")
	noMore := os.Getenv("MOCK_NOMORE") != ""
	var script []string
	if s := os.Getenv("MOCK_SCRIPT"); s != "" {
		script = strings.Split(s, ",")
	}

	// Go raw so long prompts arrive intact (see package doc). Raw mode also
	// disables output post-processing (no NL->CRNL), so we emit CRLF explicitly
	// below to keep the rendered screen aligned at column 0.
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
		}
	}

	in := bufio.NewReader(os.Stdin)
	turn := 0

	out := func(format string, a ...any) {
		s := fmt.Sprintf(format, a...)
		fmt.Print(strings.ReplaceAll(s, "\n", "\r\n"))
	}

	out("[%s] ready. prompt> ", name)

	for {
		line, err := readLine(in)
		if err != nil {
			return // stdin closed: session torn down
		}
		if strings.TrimSpace(line) == "" {
			out("[%s] prompt> ", name)
			continue
		}

		// Stream some output so idle detection has something to settle on.
		out("\n[%s] received prompt (%d chars), thinking", name, len(strings.TrimSpace(line)))
		for range 3 {
			time.Sleep(400 * time.Millisecond)
			out(".")
		}
		out("\n")

		verdict := "CLEAN"
		if turn < len(script) {
			verdict = strings.ToUpper(strings.TrimSpace(script[turn]))
		}
		turn++

		if verdict == "FIXED" && fixFile != "" {
			appendLine(fixFile, fmt.Sprintf("// %s fix at turn %d\n", name, turn))
			out("[%s] edited %s\n", name, fixFile)
		}

		out("[%s] review complete.\n", name)
		out("AUDIT_RESULT: %s\n", verdict)
		if noMore && verdict == "CLEAN" {
			out("NO_MORE_BUGS\n")
		}
		out("[%s] prompt> ", name)
	}
}

// readLine reads bytes until a CR or LF terminates a submitted line. The
// terminator is consumed and excluded. It returns an error only when the stream
// closes before any byte of the current line was read.
func readLine(in *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := in.ReadByte()
		if err != nil {
			if b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
		if c == '\r' || c == '\n' {
			return b.String(), nil
		}
		b.WriteByte(c)
	}
}

func appendLine(path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(text)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
