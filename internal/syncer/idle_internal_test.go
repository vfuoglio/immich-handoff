package syncer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestWhenIdleWaitsForTheRunningPassThenHoldsPassesBack(t *testing.T) {
	s := &Syncer{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()

	// a pass is running (it holds the lock): the wait gives up and says it was forced
	s.mu.Lock()
	start := time.Now()
	release, forced := s.WhenIdle(ctx, 40*time.Millisecond, 5*time.Millisecond)
	if !forced || time.Since(start) < 40*time.Millisecond {
		t.Errorf("a pass that never ends must force the caller after the wait: forced=%v after %v", forced, time.Since(start))
	}
	release() // forced: holds nothing, must not unlock the pass's lock
	if s.mu.TryLock() {
		t.Fatal("a forced release must not free a lock it never held")
	}
	// the pass ends while the caller waits: it gets the quiet moment, not a forced one
	go func() { time.Sleep(30 * time.Millisecond); s.mu.Unlock() }()
	release, forced = s.WhenIdle(ctx, 5*time.Second, 5*time.Millisecond)
	if forced {
		t.Fatal("a pass that ended in time must not force the caller")
	}
	// while the caller holds it, no pass can start
	if s.mu.TryLock() {
		t.Error("passes must be held back while the caller works")
	}
	release()
	if !s.mu.TryLock() {
		t.Fatal("the passes must be let go afterwards")
	}
	s.mu.Unlock()
}

func TestAWakeUpDuringTheWorkRunsRightAfterIt(t *testing.T) {
	s := &Syncer{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	release, forced := s.WhenIdle(ctx, time.Second, 5*time.Millisecond)
	if forced {
		t.Fatal("idle expected")
	}
	s.Run(ctx, "webhook", false) // arrives during the backup: cannot run now, must not be lost
	if got := s.Totals().Passes; got != 0 {
		t.Fatalf("no pass may run during the work, %d did", got)
	}
	release()
	deadline := time.Now().Add(3 * time.Second)
	for s.Totals().Passes == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Totals().Passes != 1 {
		t.Errorf("the wake-up that arrived during the backup must run right after it, passes=%d", s.Totals().Passes)
	}
}

func TestWhenIdleStopsWhenTheAppStops(t *testing.T) {
	s := &Syncer{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, forced := s.WhenIdle(ctx, time.Hour, time.Millisecond); !forced {
		t.Error("a stopping app must not keep the wait going for hours")
	}
}
