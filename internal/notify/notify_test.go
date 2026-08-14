package notify

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeNotifier records who it was asked to wake and can be made to fail.
type fakeNotifier struct {
	mu    sync.Mutex
	woken []string
	err   error
}

func (f *fakeNotifier) Notify(role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.woken = append(f.woken, role)
	return f.err
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.woken)
}

func newTestTracker(t *testing.T) (*Tracker, *fakeNotifier, *time.Time) {
	t.Helper()

	n := &fakeNotifier{}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	tracker := NewTracker(t.TempDir(), n)
	tracker.Now = func() time.Time { return now }
	tracker.RetryAfter = 15 * time.Second
	tracker.MaxAttempts = 3

	return tracker, n, &now
}

func TestNotifyRecordsSuccess(t *testing.T) {
	tracker, n, _ := newTestTracker(t)

	state, err := tracker.NotifyAndRecord("coder", "h1")
	if err != nil {
		t.Fatal(err)
	}

	if state.Status != StatusSent || state.Attempts != 1 {
		t.Errorf("state = %+v", state)
	}
	if n.count() != 1 {
		t.Errorf("notifier called %d times", n.count())
	}

	// The state is durable: a fresh tracker reads it back.
	reloaded := NewTracker(tracker.Root, n).State("coder")
	if reloaded.Status != StatusSent || reloaded.HandoffID != "h1" {
		t.Errorf("reloaded = %+v", reloaded)
	}
}

func TestNotifyRecordsFailure(t *testing.T) {
	tracker, n, _ := newTestTracker(t)
	n.err = fmt.Errorf("no session")

	state, err := tracker.NotifyAndRecord("coder", "h1")
	if err == nil {
		t.Fatal("a failed wake-up reported success")
	}
	if state.Status != StatusFailed || state.LastError == "" {
		t.Errorf("state = %+v", state)
	}
}

// The durable message must never depend on the wake-up succeeding.
func TestNotificationFailureIsInformationalOnly(t *testing.T) {
	tracker, n, _ := newTestTracker(t)
	n.err = fmt.Errorf("session missing")

	if _, err := tracker.NotifyAndRecord("coder", "h1"); err == nil {
		t.Fatal("expected an error")
	}

	// Nothing about the failure removes state or blocks a later success.
	n.err = nil
	tracker.MaxAttempts = 3

	state, err := tracker.NotifyAndRecord("coder", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusSent {
		t.Errorf("a later attempt did not recover: %+v", state)
	}
}

// Reconciliation must not fire immediately after an attempt.
func TestShouldRetryWaitsForTheInterval(t *testing.T) {
	tracker, _, now := newTestTracker(t)

	if !tracker.ShouldRetry("coder", "h1") {
		t.Error("the first notification was skipped")
	}

	if _, err := tracker.NotifyAndRecord("coder", "h1"); err != nil {
		t.Fatal(err)
	}

	if tracker.ShouldRetry("coder", "h1") {
		t.Error("an immediate retry was allowed; this is the flood case")
	}

	*now = now.Add(14 * time.Second)
	if tracker.ShouldRetry("coder", "h1") {
		t.Error("retry allowed before the interval elapsed")
	}

	*now = now.Add(2 * time.Second)
	if !tracker.ShouldRetry("coder", "h1") {
		t.Error("retry not allowed after the interval elapsed")
	}
}

// A wedged agent must not be notified forever.
func TestRetriesAreCapped(t *testing.T) {
	tracker, n, now := newTestTracker(t)
	n.err = fmt.Errorf("no session")

	for i := 0; i < tracker.MaxAttempts; i++ {
		if !tracker.ShouldRetry("coder", "h1") {
			t.Fatalf("retry %d was refused too early", i+1)
		}
		if _, err := tracker.NotifyAndRecord("coder", "h1"); err == nil {
			t.Fatal("expected failure")
		}
		*now = now.Add(time.Minute)
	}

	if tracker.ShouldRetry("coder", "h1") {
		t.Error("retries continued past the cap")
	}

	// A different message is a fresh problem and gets fresh attempts.
	if !tracker.ShouldRetry("coder", "h2") {
		t.Error("a new handoff inherited the old handoff's exhausted attempts")
	}
}

func TestSuccessStopsRetries(t *testing.T) {
	tracker, _, now := newTestTracker(t)

	if _, err := tracker.NotifyAndRecord("coder", "h1"); err != nil {
		t.Fatal(err)
	}

	// Once the role accepts the work, the tracker is cleared and nothing is
	// pending to retry.
	if err := tracker.Clear("coder"); err != nil {
		t.Fatal(err)
	}

	*now = now.Add(time.Hour)

	state := tracker.State("coder")
	if state.Status != StatusNotRequired || state.Attempts != 0 {
		t.Errorf("state after clear = %+v", state)
	}
}

func TestNewHandoffResetsAttempts(t *testing.T) {
	tracker, n, _ := newTestTracker(t)
	n.err = fmt.Errorf("boom")

	if _, err := tracker.NotifyAndRecord("coder", "h1"); err == nil {
		t.Fatal("expected failure")
	}

	n.err = nil
	state, err := tracker.NotifyAndRecord("coder", "h2")
	if err != nil {
		t.Fatal(err)
	}
	if state.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 for a new handoff", state.Attempts)
	}
}

func TestStateRejectsUnsafeRoleNames(t *testing.T) {
	tracker, _, _ := newTestTracker(t)

	for _, evil := range []string{"../escape", "a/b", "", "..", "."} {
		if _, err := tracker.NotifyAndRecord(evil, "h1"); err == nil {
			// A path-unsafe role must not create a file outside the tree.
			if _, statErr := os.Stat(filepath.Join(tracker.Root, Dir, evil+".json")); statErr == nil {
				t.Errorf("role %q wrote state outside the managed tree", evil)
			}
		}
	}
}

func TestTrackerIsSafeUnderConcurrency(t *testing.T) {
	tracker, _, _ := newTestTracker(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = tracker.NotifyAndRecord("coder", "h1")
			_ = tracker.ShouldRetry("coder", "h1")
			_ = tracker.State("coder")
		}(i)
	}
	wg.Wait()

	if state := tracker.State("coder"); state.Attempts == 0 {
		t.Error("concurrent notifications recorded nothing")
	}
}
