package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	first, held, err := TryLock(path)
	if err != nil || !held {
		t.Fatalf("first TryLock = (%v, %v)", held, err)
	}

	// A second attempt on the same file must not get it.
	second, held, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		second.Unlock()
		t.Fatal("two holders acquired the same lock")
	}

	if locked, err := IsLocked(path); err != nil || !locked {
		t.Fatalf("IsLocked = (%v, %v), want true", locked, err)
	}

	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}

	// After release it is available again.
	third, held, err := TryLock(path)
	if err != nil || !held {
		t.Fatalf("TryLock after release = (%v, %v)", held, err)
	}
	third.Unlock()
}

// A leftover lock file with nobody holding it must not block anything.
func TestStaleLockFileDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	if err := os.WriteFile(path, []byte("pid=999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if locked, err := IsLocked(path); err != nil || locked {
		t.Fatalf("a stale file reported as locked: (%v, %v)", locked, err)
	}

	lock, held, err := TryLock(path)
	if err != nil || !held {
		t.Fatalf("could not take a stale lock: (%v, %v)", held, err)
	}
	lock.Unlock()
}

func TestIsLockedOnMissingFile(t *testing.T) {
	locked, err := IsLocked(filepath.Join(t.TempDir(), "absent.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Error("a missing lock file reported as locked")
	}
}

// Two lifecycle operations must not run at once.
func TestLifecycleLockSerialisesOperations(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)

	if err := EnsureRuntimeDir(m.RepoRoot); err != nil {
		t.Fatal(err)
	}

	// Simulate another terminal holding the lifecycle lock.
	other, held, err := TryLock(LifecycleLockPath(m.RepoRoot))
	if err != nil || !held {
		t.Fatalf("could not take the lifecycle lock: (%v, %v)", held, err)
	}

	_, err = m.Start(context.Background())
	if err == nil {
		other.Unlock()
		t.Fatal("Start ran while another operation held the lifecycle lock")
	}
	if !strings.Contains(err.Error(), "another swarm lifecycle operation") {
		t.Errorf("unhelpful error: %v", err)
	}

	if _, err := m.Stop(context.Background()); err == nil {
		other.Unlock()
		t.Fatal("Stop ran while another operation held the lifecycle lock")
	}

	other.Unlock()

	// Once released, the operation proceeds.
	if _, err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start after release: %v", err)
	}
}

// Concurrent starts must not both run the pipeline.
func TestConcurrentStartsDoNotBothProceed(t *testing.T) {
	m, _, sessions, _, _ := newTestManager(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.Start(context.Background())
		}(i)
	}
	wg.Wait()

	// Whatever the interleaving, nothing may be created twice.
	if len(sessions.created) != 4 {
		t.Errorf("sessions created = %v, want exactly four", sessions.created)
	}

	// At most one may have been rejected by the lock; neither may corrupt state.
	for _, err := range errs {
		if err != nil && !strings.Contains(err.Error(), "another swarm lifecycle operation") {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestDaemonStateWithNothingRunning(t *testing.T) {
	root := t.TempDir()

	state, pid, err := DaemonState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateStopped || pid != 0 {
		t.Errorf("DaemonState = (%q, %d), want stopped", state, pid)
	}
}

// A pid file left by a dead process must not be believed.
func TestStaleDaemonRecordIsRecovered(t *testing.T) {
	root := t.TempDir()
	if err := EnsureRuntimeDir(root); err != nil {
		t.Fatal(err)
	}

	// A pid that is almost certainly not alive, and no lock held.
	record := DaemonRecord{PID: 999999, Repository: root, StartedAt: time.Now()}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(DaemonPIDPath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DaemonLockPath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}

	state, _, err := DaemonState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateStopped {
		t.Errorf("stale record reported as %q, want stopped", state)
	}

	// Stopping is a harmless no-op that tidies up.
	stopped, err := StopDaemon(root)
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Error("StopDaemon claimed to stop a dead daemon")
	}
	if _, err := os.Stat(DaemonPIDPath(root)); !os.IsNotExist(err) {
		t.Error("stale pid file was not cleaned up")
	}
}

// The daemon lock is what prevents a second daemon.
func TestDaemonCannotStartTwice(t *testing.T) {
	root := t.TempDir()

	lock, held, err := AcquireDaemonLock(root)
	if err != nil || !held {
		t.Fatalf("first AcquireDaemonLock = (%v, %v)", held, err)
	}

	_, held, err = AcquireDaemonLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("a second daemon acquired the lock")
	}

	state, pid, err := DaemonState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateRunning || pid != os.Getpid() {
		t.Errorf("DaemonState = (%q, %d), want running as this process", state, pid)
	}

	if err := ReleaseDaemonLock(root, lock); err != nil {
		t.Fatal(err)
	}

	state, _, err = DaemonState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateStopped {
		t.Errorf("after release DaemonState = %q, want stopped", state)
	}
}

// Two repositories each get their own daemon.
func TestDaemonLocksAreRepositoryScoped(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	lockA, heldA, err := AcquireDaemonLock(a)
	if err != nil || !heldA {
		t.Fatalf("repository A: (%v, %v)", heldA, err)
	}
	defer ReleaseDaemonLock(a, lockA)

	lockB, heldB, err := AcquireDaemonLock(b)
	if err != nil || !heldB {
		t.Fatalf("repository B could not start its own daemon: (%v, %v)", heldB, err)
	}
	defer ReleaseDaemonLock(b, lockB)

	stateA, _, _ := DaemonState(a)
	stateB, _, _ := DaemonState(b)
	if stateA != StateRunning || stateB != StateRunning {
		t.Errorf("states = %q / %q, want both running", stateA, stateB)
	}
}

// A real background daemon: start it, see it, stop it.
func TestStartAndStopRealDaemonProcess(t *testing.T) {
	root := t.TempDir()

	// A stand-in for the swarm binary that just sleeps while holding nothing;
	// the lock is taken by the wrapper below.
	script := filepath.Join(root, "fake-swarm")
	body := "#!/bin/sh\nexec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}

	// StartDaemon waits for the lock to be taken, which this fake never does,
	// so it must report a clear failure rather than claiming success.
	started, err := StartDaemon(root, script)
	if started {
		t.Error("StartDaemon claimed success for a process that never took the lock")
	}
	if err == nil {
		t.Fatal("StartDaemon returned no error for a daemon that never started")
	}
	if !strings.Contains(err.Error(), "did not start") {
		t.Errorf("unhelpful error: %v", err)
	}

	// The log must exist so the failure is diagnosable.
	if _, statErr := os.Stat(DaemonLogPath(root)); statErr != nil {
		t.Errorf("no daemon log was written: %v", statErr)
	}
}

func TestDaemonLogTail(t *testing.T) {
	root := t.TempDir()

	if _, err := DaemonLogTail(root, 1024); err == nil {
		t.Error("expected an error with no log present")
	}

	if err := EnsureRuntimeDir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DaemonLogPath(root), []byte("hello daemon\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := DaemonLogTail(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello daemon") {
		t.Errorf("tail = %q", out)
	}
}
