// Command aibridge runs a mutual code-review loop between two live, interactive
// CLI agents (codex and claude) on PTYs. By default it serves a web dashboard
// where you watch both agents work, edit the config, switch convergence
// strategies, and steer the run (pause / skip / single-agent / inject). Use
// --headless to run one loop in the terminal and exit (no UI).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"aibridge/internal/bridge"
	"aibridge/internal/config"
	"aibridge/internal/gitx"
	"aibridge/internal/promptlib"
	"aibridge/internal/runner"
	"aibridge/internal/server"
)

func main() {
	var (
		configPath  = flag.String("config", "aibridge.yaml", "path to the YAML config (created on save; missing is fine)")
		promptsPath = flag.String("prompts", "prompts.json", "path to the prompt-template library JSON (created on save; missing is fine)")
		repo        = flag.String("repo", "", "override the repo path from config")
		addr        = flag.String("addr", "", "override the web dashboard listen address")
		headless    = flag.Bool("headless", false, "run one review loop in the terminal and exit (no web UI)")
		noOpen      = flag.Bool("no-open", false, "do not auto-open the browser")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	if *repo != "" {
		cfg.Repo = *repo
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}

	lib, err := promptlib.Load(*promptsPath)
	if err != nil {
		fatal(err)
	}

	if *headless {
		if err := runHeadless(cfg, lib.ActiveTemplate()); err != nil {
			fatal(err)
		}
		return
	}
	if err := serve(cfg, *configPath, lib, *promptsPath, *noOpen); err != nil {
		fatal(err)
	}
}

// serve starts the web dashboard.
func serve(cfg config.Config, configPath string, lib promptlib.Library, promptsPath string, noOpen bool) error {
	srv := server.New(cfg, configPath, lib, promptsPath)
	httpSrv := &http.Server{Addr: cfg.Server.Addr, Handler: srv.Handler()}

	url := "http://" + cfg.Server.Addr
	fmt.Printf("aibridge dashboard: %s\n", url)
	if !noOpen {
		go func() { time.Sleep(400 * time.Millisecond); openBrowser(url) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// runHeadless reproduces the original one-shot CLI behavior.
func runHeadless(cfg config.Config, tmpl promptlib.Template) error {
	if !gitx.IsRepo(cfg.Repo) {
		return fmt.Errorf("%s is not a git work tree", cfg.Repo)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	run := runner.New()
	bus := run.Bus()

	// Mirror events to the terminal.
	ch, _, unsub := bus.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		for e := range ch {
			switch e.Kind {
			case bridge.EventTurnFinished:
				fmt.Printf("round %d: %s -> %s (%s)\n", e.Round, e.Side, e.Verdict, e.Message)
			case bridge.EventConverged:
				fmt.Printf("\n✓ converged: %s\n", e.Message)
			case bridge.EventStopped:
				fmt.Printf("\n■ stopped: %s\n", e.Message)
			}
			if e.Kind == bridge.EventConverged || e.Kind == bridge.EventStopped {
				close(done)
				return
			}
		}
	}()

	if err := run.Start(cfg, tmpl, runner.ResumeSet{}); err != nil {
		return err
	}
	<-done
	return nil
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "aibridge:", err)
	os.Exit(1)
}
