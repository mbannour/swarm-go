//go:build unix

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// FileLock is an advisory whole-file lock held by a live process.
//
// It uses flock(2), so ownership is a property of the operating system rather
// than of a file existing on disk: if the holder dies — cleanly, by SIGKILL, or
// in a crash — the kernel drops the lock immediately and the leftover file is
// harmless. That is what makes stale lock files recoverable here.
type FileLock struct {
	path string
	file *os.File
}

// TryLock takes the lock without blocking. held=false means someone else owns
// it right now.
func TryLock(path string) (lock *FileLock, held bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}

	return &FileLock{path: path, file: f}, true, nil
}

// Unlock releases the lock. The file itself is left in place: its existence
// carries no meaning, only the flock does.
func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}

	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil

	if err != nil {
		return err
	}

	return closeErr
}

// Write replaces the lock file's contents, so the holder can record who it is.
func (l *FileLock) Write(content string) error {
	if l == nil || l.file == nil {
		return fmt.Errorf("lock is not held")
	}

	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.WriteAt([]byte(content), 0); err != nil {
		return err
	}

	return l.file.Sync()
}

// IsLocked reports whether some live process currently holds the lock.
func IsLocked(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	lock, held, err := TryLock(path)
	if err != nil {
		return false, err
	}
	if !held {
		return true, nil
	}

	// We got it, so nobody else had it. Give it straight back.
	return false, lock.Unlock()
}

// processAlive reports whether a pid names a live process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Signal 0 performs the permission and existence checks without delivering
	// anything.
	return proc.Signal(syscall.Signal(0)) == nil
}

// terminate asks a process to stop politely.
func terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}

// kill forcibly stops a process. Callers must have verified ownership first.
func kill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGKILL)
}
