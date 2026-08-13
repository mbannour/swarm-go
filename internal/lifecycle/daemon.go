package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// daemonStopTimeout bounds how long stop waits for a polite shutdown.
const daemonStopTimeout = 5 * time.Second

// daemonStartTimeout bounds how long start waits for the daemon to take its lock.
const daemonStartTimeout = 5 * time.Second

// DaemonRecord is the identity written by a running daemon.
//
// A PID alone is not proof: pids are recycled, and after a crash the number in
// the file may belong to something unrelated. Ownership is therefore decided by
// three facts together — the process is alive, it recorded *this* repository,
// and the flock is genuinely held.
type DaemonRecord struct {
	PID        int       `json:"pid"`
	Repository string    `json:"repository"`
	StartedAt  time.Time `json:"started_at"`
}

// DaemonLockPath is the flock a live daemon holds for its whole lifetime.
func DaemonLockPath(repoRoot string) string { return runtimePath(repoRoot, daemonLockFile) }

// DaemonPIDPath is where the daemon records its identity.
func DaemonPIDPath(repoRoot string) string { return runtimePath(repoRoot, daemonPIDFile) }

// AcquireDaemonLock is called by the daemon process itself at startup. A
// held=false result means another daemon already owns this repository.
//
// The returned lock must be held for the daemon's entire life; releasing it is
// what publishes "no daemon is running here".
func AcquireDaemonLock(repoRoot string) (lock *FileLock, held bool, err error) {
	if err := EnsureRuntimeDir(repoRoot); err != nil {
		return nil, false, err
	}

	lock, held, err = TryLock(DaemonLockPath(repoRoot))
	if err != nil || !held {
		return nil, held, err
	}

	record := DaemonRecord{PID: os.Getpid(), Repository: repoRoot, StartedAt: time.Now().UTC()}

	data, err := json.Marshal(record)
	if err != nil {
		lock.Unlock()
		return nil, false, err
	}
	if err := lock.Write(string(data)); err != nil {
		lock.Unlock()
		return nil, false, err
	}
	if err := os.WriteFile(DaemonPIDPath(repoRoot), append(data, '\n'), 0o644); err != nil {
		lock.Unlock()
		return nil, false, err
	}

	return lock, true, nil
}

// ReleaseDaemonLock releases the lock and clears the pid file.
func ReleaseDaemonLock(repoRoot string, lock *FileLock) error {
	err := lock.Unlock()

	if rmErr := os.Remove(DaemonPIDPath(repoRoot)); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}

	return err
}

// readDaemonRecord loads the recorded identity, if any.
func readDaemonRecord(repoRoot string) (DaemonRecord, bool) {
	data, err := os.ReadFile(DaemonPIDPath(repoRoot))
	if err != nil {
		return DaemonRecord{}, false
	}

	var record DaemonRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return DaemonRecord{}, false
	}

	return record, true
}

// DaemonState reports whether a daemon owns this repository, and its pid.
//
// The lock is the authority. The pid is returned only when the recorded
// identity is consistent with a locked, live process belonging to this
// repository — so a stale file after a crash reports "not running" instead of
// naming a pid that may now be someone else's process.
func DaemonState(repoRoot string) (state ComponentState, pid int, err error) {
	locked, err := IsLocked(DaemonLockPath(repoRoot))
	if err != nil {
		return StateUnknown, 0, err
	}

	record, ok := readDaemonRecord(repoRoot)

	if !locked {
		// Nobody holds the lock: any pid file is stale.
		return StateStopped, 0, nil
	}

	if !ok || record.Repository != repoRoot || !processAlive(record.PID) {
		// Someone holds the lock but the identity does not line up. Report it
		// rather than guessing, and never signal an unverified pid.
		return StateFailed, 0, nil
	}

	return StateRunning, record.PID, nil
}

// CleanStaleDaemonFiles removes a pid file left behind by a dead daemon. It
// refuses to touch anything while the lock is genuinely held.
func CleanStaleDaemonFiles(repoRoot string) error {
	locked, err := IsLocked(DaemonLockPath(repoRoot))
	if err != nil {
		return err
	}
	if locked {
		return nil
	}

	if err := os.Remove(DaemonPIDPath(repoRoot)); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// StartDaemon launches the handoff daemon as a detached background process.
//
// The command is the orchestrator itself (`<swarm> handoff daemon`), started in
// its own session so it survives the terminal that ran `swarm start`, with its
// output captured to the managed log.
func StartDaemon(repoRoot, swarmBin string) (started bool, err error) {
	state, _, err := DaemonState(repoRoot)
	if err != nil {
		return false, err
	}
	if state == StateRunning {
		return false, nil
	}

	if err := CleanStaleDaemonFiles(repoRoot); err != nil {
		return false, err
	}
	if err := EnsureRuntimeDir(repoRoot); err != nil {
		return false, err
	}

	logFile, err := os.OpenFile(DaemonLogPath(repoRoot), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, err
	}
	defer logFile.Close()

	fmt.Fprintf(logFile, "\n--- handoff daemon starting at %s ---\n", time.Now().Format(time.RFC3339))

	cmd := exec.Command(swarmBin, "handoff", "daemon")
	cmd.Dir = repoRoot
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach: a new session means the daemon is not killed with the terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start handoff daemon: %w", err)
	}

	// Do not reap it here; it must outlive this process.
	go func() { _ = cmd.Wait() }()

	// Wait for it to actually take ownership, so `start` never reports a
	// daemon that immediately died (a bad binary, an unwritable directory).
	deadline := time.Now().Add(daemonStartTimeout)
	for time.Now().Before(deadline) {
		state, _, err := DaemonState(repoRoot)
		if err != nil {
			return false, err
		}
		if state == StateRunning {
			return true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	return false, fmt.Errorf(
		"handoff daemon did not start within %s; see %s",
		daemonStartTimeout, DaemonLogPath(repoRoot),
	)
}

// StopDaemon asks the daemon to stop and waits for it to let go of the lock.
//
// Only a pid verified by DaemonState is ever signalled, so this cannot
// terminate an unrelated process that happens to have reused the number.
func StopDaemon(repoRoot string) (stopped bool, err error) {
	state, pid, err := DaemonState(repoRoot)
	if err != nil {
		return false, err
	}

	switch state {
	case StateStopped:
		return false, CleanStaleDaemonFiles(repoRoot)
	case StateFailed:
		return false, fmt.Errorf(
			"a handoff daemon holds %s but its identity could not be verified; stop it manually",
			DaemonLockPath(repoRoot),
		)
	}

	if err := terminate(pid); err != nil {
		return false, fmt.Errorf("signal handoff daemon %d: %w", pid, err)
	}

	deadline := time.Now().Add(daemonStopTimeout)
	for time.Now().Before(deadline) {
		if locked, err := IsLocked(DaemonLockPath(repoRoot)); err == nil && !locked {
			return true, CleanStaleDaemonFiles(repoRoot)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// It ignored SIGTERM. Escalate, but only against the pid we verified.
	if err := kill(pid); err != nil {
		return false, fmt.Errorf("handoff daemon %d did not stop: %w", pid, err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if locked, err := IsLocked(DaemonLockPath(repoRoot)); err == nil && !locked {
			return true, CleanStaleDaemonFiles(repoRoot)
		}
		time.Sleep(50 * time.Millisecond)
	}

	return false, fmt.Errorf("handoff daemon %d is still running", pid)
}

// DaemonLogTail returns the last n bytes of the managed daemon log.
func DaemonLogTail(repoRoot string, limit int64) (string, error) {
	path := DaemonLogPath(repoRoot)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no daemon log yet at %s", filepath.Clean(path))
		}
		return "", err
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	offset := int64(0)
	if info.Size() > limit {
		offset = info.Size() - limit
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return "", err
	}

	buf := make([]byte, info.Size()-offset)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}

	return string(buf[:n]), nil
}
