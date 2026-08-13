package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Check is one preflight result.
type Check struct {
	Name string
	Err  error
}

// OK reports whether the check passed.
func (c Check) OK() bool { return c.Err == nil }

// Preflight validates everything it can before any runtime state is touched.
//
// The point is to fail before starting anything, rather than half-way through.
func (m *Manager) Preflight() []Check {
	var checks []Check

	add := func(name string, err error) { checks = append(checks, Check{Name: name, Err: err}) }

	// Repository and configuration were already resolved to build the Manager,
	// so record them and check what remains.
	add("Git repository", m.checkRepo())
	add("configuration", m.checkRoles())
	add("worktree paths", m.checkWorktreePaths())
	add("tmux", m.checkTmux())
	add("agent backends", m.checkBackends())
	add("prompts", m.checkPrompts())
	add("runtime directory", m.checkRuntimeDir())

	return checks
}

func (m *Manager) checkRepo() error {
	if m.RepoRoot == "" {
		return fmt.Errorf("no repository root")
	}
	if info, err := os.Stat(filepath.Join(m.RepoRoot, ".git")); err != nil || (!info.IsDir() && info.Size() == 0) {
		return fmt.Errorf("%s does not look like a Git repository", m.RepoRoot)
	}
	return nil
}

func (m *Manager) checkRoles() error {
	if len(m.Roles) == 0 {
		return fmt.Errorf("no roles configured in swarm.conf")
	}

	seen := map[string]bool{}
	for _, r := range m.Roles {
		if r.Name == "" || r.Backend == "" || r.WorktreeName == "" {
			return fmt.Errorf("role %q is incomplete", r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("role %q is configured twice", r.Name)
		}
		seen[r.Name] = true
	}

	return nil
}

// checkWorktreePaths verifies that every worktree stays inside the repository's
// managed area — a configuration typo must not point a role at /etc.
func (m *Manager) checkWorktreePaths() error {
	base := filepath.Join(m.RepoRoot, ".swarm", "worktrees")

	for _, r := range m.Roles {
		if r.Worktree == "" {
			return fmt.Errorf("role %q has no worktree path", r.Name)
		}
		rel, err := filepath.Rel(base, r.Worktree)
		if err != nil || rel == ".." || filepath.IsAbs(rel) ||
			(len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)) {
			return fmt.Errorf("worktree for %q is outside %s", r.Name, base)
		}
	}

	return nil
}

func (m *Manager) checkTmux() error {
	if m.Env == nil || !m.Env.TmuxAvailable() {
		return fmt.Errorf("tmux is not installed or not available in PATH")
	}
	return nil
}

func (m *Manager) checkBackends() error {
	seen := map[string]bool{}

	for _, r := range m.Roles {
		if seen[r.Backend] {
			continue
		}
		seen[r.Backend] = true

		if !m.Env.BackendAvailable(r.Backend) {
			return fmt.Errorf("%s executable not found (configured for role %q)", r.Backend, r.Name)
		}
	}

	return nil
}

func (m *Manager) checkPrompts() error {
	for _, r := range m.Roles {
		if err := m.Env.PromptsPresent(r.Name); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) checkRuntimeDir() error {
	if err := EnsureRuntimeDir(m.RepoRoot); err != nil {
		return err
	}

	probe := runtimePath(m.RepoRoot, ".writable")
	if err := os.WriteFile(probe, []byte("ok\n"), 0o644); err != nil {
		return fmt.Errorf("runtime directory is not writable: %w", err)
	}

	return os.Remove(probe)
}

// StartReport records what one start invocation did.
type StartReport struct {
	Checks    []Check
	Steps     []Step
	Status    SwarmStatus
	Complete  bool
	AlreadyUp bool
}

// Step is one component the start pipeline touched.
type Step struct {
	Name    string
	Created bool // true when this invocation brought it up
	Err     error
}

// Start brings the swarm up in a deterministic order:
//
//	preflight → runtime dir → worktrees → daemon → sessions → agents → health
//
// Worktrees come before sessions because a session starts inside its worktree;
// the daemon comes before agents so that a handoff produced by an agent's first
// action is delivered immediately.
//
// Startup is repairing rather than transactional (Policy B): components that
// are already up are left alone, missing ones are created, and a failure is
// reported honestly with whatever succeeded left running so the next `swarm
// start` can finish the job.
func (m *Manager) Start(ctx context.Context) (StartReport, error) {
	var report StartReport

	err := m.withLifecycleLock("start", func() error {
		report.Checks = m.Preflight()

		failed := false
		for _, c := range report.Checks {
			if !c.OK() {
				failed = true
			}
		}
		if failed {
			return fmt.Errorf("preflight failed")
		}

		before, err := m.Status(ctx)
		if err != nil {
			return err
		}
		report.AlreadyUp = before.Running() && before.Health == HealthHealthy

		m.startComponents(ctx, &report)

		after, statusErr := m.Status(ctx)
		if statusErr != nil {
			return statusErr
		}
		report.Status = after

		if err := writeMetadata(m.RepoRoot, Metadata{
			Repository: m.RepoRoot,
			StartedAt:  time.Now().UTC(),
			Roles:      roleNames(m.Roles),
		}); err != nil {
			m.verbosef("could not record runtime metadata: %v\n", err)
		}

		report.Complete = true
		for _, s := range report.Steps {
			if s.Err != nil {
				report.Complete = false
			}
		}
		if after.Health == HealthFailed {
			report.Complete = false
		}

		if !report.Complete {
			return fmt.Errorf("swarm startup incomplete")
		}

		return nil
	})

	return report, err
}

// startComponents runs the ordered pipeline, recording each outcome. It keeps
// going after a per-role failure so the report describes the whole swarm.
func (m *Manager) startComponents(ctx context.Context, report *StartReport) {
	record := func(name string, created bool, err error) {
		report.Steps = append(report.Steps, Step{Name: name, Created: created, Err: err})
	}

	// 1. Worktrees: a session cannot start without one.
	for _, r := range m.Roles {
		if ctx.Err() != nil {
			return
		}
		created, err := m.Worktrees.Ensure(r)
		record("worktree:"+r.Name, created, err)
	}

	// 2. Handoff daemon: running before agents, so their first handoff moves.
	if !m.SkipDaemon {
		if ctx.Err() != nil {
			return
		}
		started, err := m.startDaemon()
		record("daemon", started, err)
	}

	// 3. Sessions, then 4. agents — per role, so one broken role does not stop
	//    the others from coming up.
	for _, r := range m.Roles {
		if ctx.Err() != nil {
			return
		}

		created, err := m.Sessions.Ensure(r)
		record("session:"+r.Name, created, err)
		if err != nil {
			continue
		}

		started, err := m.Agents.Ensure(r)
		record("agent:"+r.Name, started, err)
	}
}

// startDaemon launches the managed background daemon.
func (m *Manager) startDaemon() (bool, error) {
	bin, err := m.Env.SwarmBinary()
	if err != nil {
		return false, err
	}
	return StartDaemon(m.RepoRoot, bin)
}

func roleNames(roles []Role) []string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names
}
