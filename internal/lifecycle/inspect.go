package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mbannour/swarm-go/internal/diagnostics"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/repair"
)

// tempFileAge is how old a leftover temporary file must be before it is
// considered abandoned rather than in flight.
const tempFileAge = 10 * time.Minute

// Inspector answers the diagnostics package's questions about a live swarm, and
// performs the repairs the repair package plans. It observes through the same
// components the lifecycle uses; nothing here reimplements them.
type Inspector struct {
	Mgr *Manager
	// Git answers worktree questions that need Git itself.
	Git GitInspector
	// Handoffs answers durable-state questions.
	Handoffs HandoffInspector
	// Tmux answers server-level questions.
	Tmux TmuxInspector
}

// GitInspector inspects worktrees with Git.
type GitInspector interface {
	Inspect(role, worktreeName, branch, path string) (diagnostics.Worktree, error)
	StaleMetadata() ([]string, error)
	Prune() error
}

// HandoffInspector inspects durable handoff state.
type HandoffInspector interface {
	CurrentCount(role string) (int, error)
	FailedCount(role string) (int, error)
	RejectedCount() (int, error)
	PendingOutbox(role string) (int, error)
	Orphans(roles []string) ([]diagnostics.Orphan, error)
	Reconcile(role, id string) error
}

// TmuxInspector answers questions about the tmux server itself.
type TmuxInspector interface {
	SocketPath() string
	ServerAlive() bool
	RemoveSocket() error
}

// ---- diagnostics.System -------------------------------------------------

func (i *Inspector) RepoRoot() string { return i.Mgr.RepoRoot }

func (i *Inspector) Roles() []diagnostics.Role {
	out := make([]diagnostics.Role, 0, len(i.Mgr.Roles))
	for _, r := range i.Mgr.Roles {
		out = append(out, diagnostics.Role{
			Name: r.Name, Backend: r.Backend, ReceiveMode: r.ReceiveMode,
		})
	}
	return out
}

func (i *Inspector) RepoPresent() error { return i.Mgr.checkRepo() }
func (i *Inspector) ConfigValid() error { return i.Mgr.checkRoles() }
func (i *Inspector) RuntimeWritable() error {
	// Read-only probe: report whether the directory could be written, without
	// creating the tree as a side effect of asking.
	dir := filepath.Join(i.Mgr.RepoRoot, filepath.FromSlash(RuntimeDir))
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil // it will be created on the next start
		}
		return err
	}

	probe := filepath.Join(dir, ".writable")
	if err := os.WriteFile(probe, []byte("ok\n"), 0o644); err != nil {
		return fmt.Errorf("runtime directory is not writable: %w", err)
	}

	return os.Remove(probe)
}

func (i *Inspector) HasCommits() bool {
	if i.Git == nil {
		return true
	}
	// Delegated through the worktree inspector's repository.
	return hasCommits(i.Mgr.RepoRoot)
}

func (i *Inspector) PromptsPresent(role string) error { return i.Mgr.Env.PromptsPresent(role) }

func (i *Inspector) BackendAvailable(backend string) bool {
	return i.Mgr.Env.BackendAvailable(backend)
}

func (i *Inspector) TmuxAvailable() bool { return i.Mgr.Env.TmuxAvailable() }

func (i *Inspector) SocketPresent() bool {
	if i.Tmux == nil {
		return false
	}
	_, err := os.Stat(i.Tmux.SocketPath())
	return err == nil
}

func (i *Inspector) ServerAlive() bool {
	if i.Tmux == nil {
		return false
	}
	return i.Tmux.ServerAlive()
}

func (i *Inspector) SessionAlive(role string) (bool, error) {
	r, err := i.role(role)
	if err != nil {
		return false, err
	}
	return i.Mgr.Sessions.Present(r)
}

func (i *Inspector) AgentState(role string) (string, error) {
	r, err := i.role(role)
	if err != nil {
		return "", err
	}
	return i.Mgr.Agents.State(r)
}

func (i *Inspector) InspectWorktree(role string) (diagnostics.Worktree, error) {
	r, err := i.role(role)
	if err != nil {
		return diagnostics.Worktree{}, err
	}
	if i.Git == nil {
		return diagnostics.Worktree{State: diagnostics.WorktreeHealthy}, nil
	}
	return i.Git.Inspect(r.Name, r.WorktreeName, r.Branch, r.Worktree)
}

func (i *Inspector) StaleWorktreeMetadata() ([]string, error) {
	if i.Git == nil {
		return nil, nil
	}
	return i.Git.StaleMetadata()
}

func (i *Inspector) DaemonState() (string, int, error) {
	state, pid, err := DaemonState(i.Mgr.RepoRoot)
	return string(state), pid, err
}

func (i *Inspector) DaemonPIDFilePresent() bool {
	_, err := os.Stat(DaemonPIDPath(i.Mgr.RepoRoot))
	return err == nil
}

func (i *Inspector) LifecycleLockHeld() (bool, error) {
	return IsLocked(LifecycleLockPath(i.Mgr.RepoRoot))
}

func (i *Inspector) CurrentCount(role string) (int, error) {
	if i.Handoffs == nil {
		return 0, nil
	}
	return i.Handoffs.CurrentCount(role)
}

func (i *Inspector) FailedCount(role string) (int, error) {
	if i.Handoffs == nil {
		return 0, nil
	}
	return i.Handoffs.FailedCount(role)
}

func (i *Inspector) RejectedCount() (int, error) {
	if i.Handoffs == nil {
		return 0, nil
	}
	return i.Handoffs.RejectedCount()
}

func (i *Inspector) PendingOutbox(role string) (int, error) {
	if i.Handoffs == nil {
		return 0, nil
	}
	return i.Handoffs.PendingOutbox(role)
}

func (i *Inspector) Orphans() ([]diagnostics.Orphan, error) {
	if i.Handoffs == nil {
		return nil, nil
	}
	return i.Handoffs.Orphans(roleNames(i.Mgr.Roles))
}

// StaleTempFiles finds abandoned atomic-write temporaries, and only inside the
// managed .swarm tree — never anywhere else in the repository.
func (i *Inspector) StaleTempFiles() ([]string, error) {
	base := filepath.Join(i.Mgr.RepoRoot, ".swarm")

	var found []string
	cutoff := time.Now().Add(-tempFileAge)

	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return nil // an unreadable corner is not worth failing the scan
		}
		if info.IsDir() {
			return nil
		}
		// Only the patterns this project writes.
		if !strings.HasPrefix(filepath.Base(path), ".tmp-") {
			return nil
		}
		if info.ModTime().After(cutoff) {
			return nil // possibly a write in flight
		}
		found = append(found, path)
		return nil
	})

	return found, err
}

// ---- repair.Actuator ----------------------------------------------------

func (i *Inspector) ClearDaemonMetadata() error {
	return CleanStaleDaemonFiles(i.Mgr.RepoRoot)
}

func (i *Inspector) StartDaemon() error {
	_, err := i.Mgr.startDaemon()
	return err
}

// RemoveStaleSocket deletes the tmux socket only after confirming that no
// server answers through it, and only this project's socket.
func (i *Inspector) RemoveStaleSocket() error {
	if i.Tmux == nil {
		return nil
	}
	if i.Tmux.ServerAlive() {
		return fmt.Errorf("refusing to remove a socket with a live tmux server behind it")
	}
	return i.Tmux.RemoveSocket()
}

func (i *Inspector) CreateSession(role string) error {
	r, err := i.role(role)
	if err != nil {
		return err
	}
	_, err = i.Mgr.Sessions.Ensure(r)
	return err
}

func (i *Inspector) StartAgent(role string) error {
	r, err := i.role(role)
	if err != nil {
		return err
	}
	_, err = i.Mgr.Agents.Ensure(r)
	return err
}

func (i *Inspector) CreateWorktree(role string) error {
	r, err := i.role(role)
	if err != nil {
		return err
	}
	_, err = i.Mgr.Worktrees.Ensure(r)
	return err
}

func (i *Inspector) PruneWorktrees() error {
	if i.Git == nil {
		return nil
	}
	return i.Git.Prune()
}

func (i *Inspector) ReconcileOrphan(role, id string) error {
	if i.Handoffs == nil {
		return nil
	}
	return i.Handoffs.Reconcile(role, id)
}

func (i *Inspector) CleanTempFiles() (int, error) {
	files, err := i.StaleTempFiles()
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, path := range files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed++
	}

	return removed, nil
}

func (i *Inspector) role(name string) (Role, error) {
	for _, r := range i.Mgr.Roles {
		if r.Name == name {
			return r, nil
		}
	}
	return Role{}, fmt.Errorf("unknown role %q", name)
}

// Diagnose runs a full read-only inspection.
func (i *Inspector) Diagnose() diagnostics.Report { return diagnostics.Detect(i) }

// Repair diagnoses, plans and (unless dryRun) applies the plan. It holds the
// lifecycle lock, so it cannot race with start, stop or another repair.
func (i *Inspector) Repair(dryRun bool) (plan repair.Plan, report repair.Report, err error) {
	err = i.Mgr.withLifecycleLock("repair", func() error {
		diagnosis := i.Diagnose()
		plan = repair.PlanFrom(diagnosis)

		executor := &repair.Executor{Actuator: i, Log: i.Mgr.Out}
		report = executor.Execute(plan, dryRun)

		return nil
	})

	return plan, report, err
}

// compile-time checks that the adapters satisfy both contracts.
var (
	_ diagnostics.System = (*Inspector)(nil)
	_ repair.Actuator    = (*Inspector)(nil)
)

// handoffBoxes is re-exported for the adapters in this package.
var handoffBoxes = []string{
	handoff.BoxInbox, handoff.BoxOutbox, handoff.BoxSent,
	handoff.BoxFailed, handoff.BoxCurrent, handoff.BoxCompleted,
}
