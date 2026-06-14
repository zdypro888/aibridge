package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControlResumeHasNoLostWakeup(t *testing.T) {
	ctrl := NewControl()
	ctrl.Pause()
	ctrl.Resume()

	allowed, injected, err := ctrl.gateBeforeTurn(context.Background(), "codex")
	if err != nil {
		t.Fatalf("gate after pre-resume returned error: %v", err)
	}
	if !allowed || injected != "" {
		t.Fatalf("gate after pre-resume = allowed %v inject %q, want allowed with no inject", allowed, injected)
	}

	ctrl.Pause()
	done := make(chan error, 1)
	go func() {
		allowed, injected, err := ctrl.gateBeforeTurn(context.Background(), "codex")
		if err != nil {
			done <- err
			return
		}
		if !allowed || injected != "" {
			done <- errors.New("unexpected gate result after resume")
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("gate returned before resume while paused")
		}
	default:
	}

	ctrl.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gate after resume failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gate did not wake after resume")
	}
}

func TestControlAbortUnblocksPausedGate(t *testing.T) {
	ctrl := NewControl()
	ctrl.Pause()

	done := make(chan error, 1)
	go func() {
		_, _, err := ctrl.gateBeforeTurn(context.Background(), "claude")
		done <- err
	}()

	ctrl.Abort()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("gate after abort error = %v, want aborted error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("abort did not wake paused gate")
	}
}

func TestControlPausedGateReturnsOnContextCancel(t *testing.T) {
	ctrl := NewControl()
	ctrl.Pause()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, _, err := ctrl.gateBeforeTurn(ctx, "codex")
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("gate after context cancel error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancel did not wake paused gate")
	}
}

func TestControlInjectSurvivesOnlySideAndSkipOnce(t *testing.T) {
	ctrl := NewControl()
	ctrl.Inject("codex", "manual text")

	ctrl.OnlySide("claude")
	allowed, injected, err := ctrl.gateBeforeTurn(context.Background(), "codex")
	if err != nil {
		t.Fatalf("gate with only-side returned error: %v", err)
	}
	if allowed || injected != "" {
		t.Fatalf("gate with only-side = allowed %v inject %q, want skipped with no inject", allowed, injected)
	}

	ctrl.OnlySide("")
	ctrl.SkipNext("codex")
	allowed, injected, err = ctrl.gateBeforeTurn(context.Background(), "codex")
	if err != nil {
		t.Fatalf("gate with skip returned error: %v", err)
	}
	if allowed || injected != "" {
		t.Fatalf("gate with skip = allowed %v inject %q, want skipped with no inject", allowed, injected)
	}

	allowed, injected, err = ctrl.gateBeforeTurn(context.Background(), "codex")
	if err != nil {
		t.Fatalf("gate after skip returned error: %v", err)
	}
	if !allowed || injected != "manual text" {
		t.Fatalf("gate after skip = allowed %v inject %q, want queued inject", allowed, injected)
	}

	allowed, injected, err = ctrl.gateBeforeTurn(context.Background(), "codex")
	if err != nil {
		t.Fatalf("second gate after inject returned error: %v", err)
	}
	if !allowed || injected != "" {
		t.Fatalf("second gate after inject = allowed %v inject %q, want no repeated inject", allowed, injected)
	}
}

func TestControlConcurrentStateAndMutatorsRace(t *testing.T) {
	ctrl := NewControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			side := "codex"
			if i%2 == 1 {
				side = "claude"
			}
			for j := 0; j < 200; j++ {
				switch j % 7 {
				case 0:
					ctrl.Pause()
				case 1:
					ctrl.Resume()
				case 2:
					ctrl.SkipNext(side)
				case 3:
					ctrl.OnlySide(side)
				case 4:
					ctrl.OnlySide("")
				case 5:
					ctrl.Inject(side, "text")
				case 6:
					_ = ctrl.State()
				}
			}
		}()
	}

	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			side := "codex"
			if i%2 == 1 {
				side = "claude"
			}
			for j := 0; j < 100; j++ {
				_, _, err := ctrl.gateBeforeTurn(ctx, side)
				if err != nil {
					return
				}
			}
		}()
	}

	timer := time.AfterFunc(50*time.Millisecond, func() {
		cancel()
		ctrl.Abort()
	})
	defer timer.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent control operations did not finish")
	}
}
