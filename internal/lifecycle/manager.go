package lifecycle

import (
	"fmt"
	"io"
	"os"
)

// The lifecycle reaches every component through one of these interfaces. They
// exist so orchestration can be tested without tmux, Git or an AI backend, and
// so the lifecycle never absorbs a component's implementation details.

// WorktreeService manages the per-role Git worktrees.
type WorktreeService interface {
	// Ensure creates the role's worktree if it is missing.
	Ensure(r Role) (created bool, err error)
	// Present reports whether the worktree exists and is registered.
	Present(r Role) (bool, error)
}

// SessionService manages the per-role tmux sessions.
type SessionService interface {
	Ensure(r Role) (created bool, err error)
	Present(r Role) (bool, error)
	Remove(r Role) (removed bool, err error)
}

// AgentService manages the coding-agent process inside a session.
type AgentService interface {
	// Ensure starts the agent if the session has none running.
	Ensure(r Role) (started bool, err error)
	Stop(r Role) (stopped bool, err error)
	// State is "running", "not-started", "session-missing" or "backend-missing".
	State(r Role) (string, error)
}

// WorkService exposes the durable handoff lifecycle.
type WorkService interface {
	// Work returns the role's work state and its current task, if any.
	Work(role string) (state string, task string, err error)
	// Counts summarises durable handoff state across all roles.
	Counts() (Counts, error)
	// Notification reports the last wake-up attempt for a role.
	Notification(role string) (status string, attempts int, lastError string)
}

// Environment answers preflight questions about the machine.
type Environment interface {
	TmuxAvailable() bool
	BackendAvailable(backend string) bool
	// PromptsPresent reports whether every prompt a role needs exists.
	PromptsPresent(role string) error
	// SwarmBinary returns a stable executable path for background components.
	SwarmBinary() (string, error)
	// BackendReady reports whether a backend can actually start working for a
	// role under its configured policy — not merely whether it is installed.
	BackendReady(backend, approval string) (state string, reason string)
}

// Manager coordinates the components. It owns ordering and locking; the
// components own their own behavior.
type Manager struct {
	RepoRoot string
	Roles    []Role

	Worktrees WorktreeService
	Sessions  SessionService
	Agents    AgentService
	Work      WorkService
	Env       Environment

	// Out receives progress lines. Diagnostics go to the caller's stderr.
	Out io.Writer
	// Verbose adds per-step detail.
	Verbose bool
	// SkipDaemon disables background daemon management (used by tests).
	SkipDaemon bool
}

func (m *Manager) out() io.Writer {
	if m.Out == nil {
		return io.Discard
	}
	return m.Out
}

func (m *Manager) printf(format string, args ...interface{}) {
	fmt.Fprintf(m.out(), format, args...)
}

func (m *Manager) verbosef(format string, args ...interface{}) {
	if m.Verbose {
		fmt.Fprintf(m.out(), format, args...)
	}
}

// LifecycleLockPath is the lock held for the duration of start and stop, so two
// terminals cannot drive the same swarm at the same time.
func LifecycleLockPath(repoRoot string) string { return runtimePath(repoRoot, lifecycleLock) }

// withLifecycleLock runs fn while holding the lifecycle lock.
func (m *Manager) withLifecycleLock(operation string, fn func() error) error {
	if err := EnsureRuntimeDir(m.RepoRoot); err != nil {
		return err
	}

	lock, held, err := TryLock(LifecycleLockPath(m.RepoRoot))
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf(
			"another swarm lifecycle operation is running for this repository; "+
				"wait for it to finish, then retry `swarm %s`",
			operation,
		)
	}
	defer lock.Unlock()

	if err := lock.Write(fmt.Sprintf("%s pid=%d\n", operation, os.Getpid())); err != nil {
		return err
	}

	return fn()
}
