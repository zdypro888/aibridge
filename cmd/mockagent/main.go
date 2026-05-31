// Command mockagent is a fake interactive coding agent used to end-to-end test
// the bridge's PTY drive + idle detection + convergence without burning real
// codex/claude turns. It behaves like a real TUI: prints a prompt, blocks on a
// line of stdin, "thinks" (sleeps so the screen streams), optionally edits a
// file, prints a report ending with AUDIT_RESULT (and optionally NO_MORE_BUGS),
// then loops.
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
)

func main() {
	name := envOr("MOCK_NAME", "mock")
	fixFile := os.Getenv("MOCK_FIX_FILE")
	noMore := os.Getenv("MOCK_NOMORE") != ""
	var script []string
	if s := os.Getenv("MOCK_SCRIPT"); s != "" {
		script = strings.Split(s, ",")
	}

	in := bufio.NewReader(os.Stdin)
	turn := 0
	fmt.Printf("[%s] ready. prompt> ", name)

	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return // stdin closed: session torn down
		}
		if strings.TrimSpace(line) == "" {
			fmt.Printf("[%s] prompt> ", name)
			continue
		}

		// Stream some output so idle detection has something to settle on.
		fmt.Printf("\n[%s] received prompt (%d chars), thinking", name, len(strings.TrimSpace(line)))
		for range 3 {
			time.Sleep(400 * time.Millisecond)
			fmt.Print(".")
		}
		fmt.Println()

		verdict := "CLEAN"
		if turn < len(script) {
			verdict = strings.ToUpper(strings.TrimSpace(script[turn]))
		}
		turn++

		if verdict == "FIXED" && fixFile != "" {
			appendLine(fixFile, fmt.Sprintf("// %s fix at turn %d\n", name, turn))
			fmt.Printf("[%s] edited %s\n", name, fixFile)
		}

		fmt.Printf("[%s] review complete.\n", name)
		fmt.Printf("AUDIT_RESULT: %s\n", verdict)
		if noMore && verdict == "CLEAN" {
			fmt.Println("NO_MORE_BUGS")
		}
		fmt.Printf("[%s] prompt> ", name)
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
