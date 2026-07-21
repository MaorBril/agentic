package router

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// touch writes content to path with a fresh mtime, guaranteeing the mtime
// advances past a prior read even on filesystems with coarse (1s) mtime
// resolution.
func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Force mtime strictly above now so the next poll sees a change even on
	// 1s-resolution filesystems (HFS+, some CI tmpfs).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

// TestPollReloadFiresOnChange verifies a write to the watched file triggers
// exactly one reload, and a second write triggers a second.
func TestPollReloadFiresOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	touch(t, path, "v1")

	var calls atomic.Int32
	reload := func() error { calls.Add(1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollReload(ctx, quietLogger(), path, 10*time.Millisecond, reload)

	// Baseline: no reloads yet.
	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("spurious reload before any change: %d", got)
	}

	touch(t, path, "v2")
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })

	touch(t, path, "v3")
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
}

// TestPollReloadSurvivesBadEdit verifies a reload error is logged and skipped
// without stopping the watcher — the next good edit reloads again.
func TestPollReloadSurvivesBadEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	touch(t, path, "v1")

	var calls atomic.Int32
	var fail atomic.Bool
	reload := func() error {
		calls.Add(1)
		if fail.Load() {
			return errReloadSimulated
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollReload(ctx, quietLogger(), path, 10*time.Millisecond, reload)

	// First change: reload fails. Watcher must survive.
	fail.Store(true)
	touch(t, path, "broken")
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })

	// Second change: reload succeeds. Watcher picked it back up.
	fail.Store(false)
	touch(t, path, "fixed")
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
}

// TestPollReloadStopsOnContext verifies canceling ctx unblocks pollReload.
func TestPollReloadStopsOnContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	touch(t, path, "v1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pollReload(ctx, quietLogger(), path, 10*time.Millisecond, func() error { return nil })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pollReload did not return after ctx cancel")
	}
}

var errReloadSimulated = errSentinel("simulated reload failure")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

func waitFor(t *testing.T, max time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", max)
}
