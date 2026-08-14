package diagnostics

import (
	"fmt"
	"sort"
)

// WorktreeState is what an inspection of one role's worktree found.
type WorktreeState string

const (
	WorktreeHealthy  WorktreeState = "healthy"
	WorktreeMissing  WorktreeState = "missing"
	WorktreeDirty    WorktreeState = "dirty"
	WorktreeDetached WorktreeState = "detached"
	WorktreeWrongRef WorktreeState = "wrong-branch"
	WorktreeInvalid  WorktreeState = "invalid-git-metadata"
)

// Worktree is one worktree inspection result.
type Worktree struct {
	State          WorktreeState
	Path           string
	ExpectedBranch string
	ActualBranch   string
	// Registered reports whether Git tracks this path as a worktree.
	Registered bool
}

// Orphan is a delivered handoff whose sender-side lifecycle never completed.
type Orphan struct {
	Role         string // the sender
	ID           string
	Path         string // the source file still sitting in the outbox
	Destinations []string
}

// Role is the subset of configuration the detector needs.
type Role struct {
	Name        string
	Backend     string
	ReceiveMode string
}

// System is everything the detector can observe. Every method is read-only.
//
// The interface exists so failures can be simulated exactly in tests: there is
// no way to reach a crashed daemon or a corrupted worktree reliably otherwise.
type System interface {
	RepoRoot() string
	Roles() []Role

	// Repository and configuration.
	RepoPresent() error
	HasCommits() bool
	ConfigValid() error
	PromptsPresent(role string) error
	RuntimeWritable() error

	// Git worktrees.
	InspectWorktree(role string) (Worktree, error)
	StaleWorktreeMetadata() ([]string, error)

	// tmux and agents.
	TmuxAvailable() bool
	SocketPresent() bool
	ServerAlive() bool
	SessionAlive(role string) (bool, error)
	AgentState(role string) (string, error)
	BackendAvailable(backend string) bool

	// Handoff daemon and locks.
	DaemonState() (state string, pid int, err error)
	DaemonPIDFilePresent() bool
	LifecycleLockHeld() (bool, error)

	// Durable handoff state.
	CurrentCount(role string) (int, error)
	FailedCount(role string) (int, error)
	RejectedCount() (int, error)
	PendingOutbox(role string) (int, error)
	Orphans() ([]Orphan, error)
	StaleTempFiles() ([]string, error)
}

// Detect inspects a system and returns everything it found.
func Detect(s System) Report {
	report := Report{Repository: s.RepoRoot()}

	var diags []Diagnostic
	add := func(d Diagnostic) { diags = append(diags, d) }

	// ---- repository and configuration --------------------------------
	if err := s.RepoPresent(); err != nil {
		add(Diagnostic{
			Code: CodeRepoMissing, Severity: SeverityCritical, Component: "repository",
			Message: err.Error(),
		})
		// Nothing else can be trusted without a repository.
		report.Diagnostics = diags
		report.Health = HealthBlocked
		return report
	}

	if !s.HasCommits() {
		add(Diagnostic{
			Code: CodeRepoNoCommits, Severity: SeverityError, Component: "repository",
			Message: "repository has no commits yet; create an initial commit before starting",
		})
	}

	if err := s.ConfigValid(); err != nil {
		add(Diagnostic{
			Code: CodeConfigInvalid, Severity: SeverityCritical, Component: "configuration",
			Message: err.Error(),
		})
	}

	if err := s.RuntimeWritable(); err != nil {
		add(Diagnostic{
			Code: CodeRuntimeNotWrite, Severity: SeverityError, Component: "runtime",
			Message: err.Error(),
		})
	}

	// ---- tmux server -------------------------------------------------
	serverAlive := false
	if !s.TmuxAvailable() {
		add(Diagnostic{
			Code: CodeTmuxMissing, Severity: SeverityError, Component: "tmux",
			Message: "tmux is not installed or not available in PATH",
		})
	} else {
		serverAlive = s.ServerAlive()

		// A socket file proves nothing: only a response from the server does.
		if s.SocketPresent() && !serverAlive {
			add(Diagnostic{
				Code: CodeSocketStale, Severity: SeverityWarning, Component: "tmux",
				Message:    "the tmux socket file exists but no server answers through it",
				Repairable: true,
			})
		}
	}

	// ---- daemon ------------------------------------------------------
	daemonState, pid, err := s.DaemonState()
	switch {
	case err != nil:
		add(Diagnostic{
			Code: CodeDaemonNotRunning, Severity: SeverityError, Component: "daemon",
			Message: fmt.Sprintf("cannot determine daemon state: %v", err),
		})
	case daemonState == "running":
		// Healthy.
	case daemonState == "failed":
		add(Diagnostic{
			Code: CodeDaemonUnverified, Severity: SeverityError, Component: "daemon",
			Message: "a process holds the daemon lock but its identity could not be verified; " +
				"stop it manually — swarm will not signal an unverified process",
		})
	default:
		// Stopped. Only a problem when the rest of the swarm is up, but the
		// stale pid file is worth clearing either way.
		if s.DaemonPIDFilePresent() {
			add(Diagnostic{
				Code: CodeDaemonStalePID, Severity: SeverityWarning, Component: "daemon",
				Message:    "daemon metadata names a process that is not running",
				Repairable: true,
			})
		}
	}
	_ = pid

	// ---- per-role ----------------------------------------------------
	anySession := false
	roles := s.Roles()

	for _, role := range roles {
		if err := s.PromptsPresent(role.Name); err != nil {
			add(Diagnostic{
				Code: CodePromptsMissing, Severity: SeverityError, Component: role.Name,
				Message: err.Error(),
			})
		}

		if !s.BackendAvailable(role.Backend) {
			add(Diagnostic{
				Code: CodeBackendMissing, Severity: SeverityError, Component: role.Name,
				Message: fmt.Sprintf("backend %q is not available", role.Backend),
				Detail:  map[string]string{"backend": role.Backend},
			})
		}

		diags = append(diags, inspectWorktree(s, role)...)

		live := false
		if serverAlive {
			var err error
			live, err = s.SessionAlive(role.Name)
			if err == nil && live {
				anySession = true
			}
		}

		diags = append(diags, inspectRuntime(s, role, serverAlive, live)...)
		diags = append(diags, inspectWork(s, role)...)
	}

	// ---- daemon down while the swarm is otherwise up -------------------
	if daemonState == "stopped" && anySession {
		add(Diagnostic{
			Code: CodeDaemonNotRunning, Severity: SeverityError, Component: "daemon",
			Message:    "the handoff daemon is not running; handoffs will not be delivered",
			Repairable: true,
		})
	}

	// ---- shared handoff state -----------------------------------------
	if n, err := s.RejectedCount(); err == nil && n > 0 {
		add(Diagnostic{
			Code: CodeHandoffRejected, Severity: SeverityWarning, Component: "handoffs",
			Message: fmt.Sprintf("%d rejected handoff(s) are quarantined; inspect the .reason files", n),
			Detail:  map[string]string{"count": fmt.Sprint(n)},
		})
	}

	if orphans, err := s.Orphans(); err == nil {
		for _, o := range orphans {
			add(Diagnostic{
				Code: CodeOrphanDelivery, Severity: SeverityWarning, Component: o.Role,
				Message: fmt.Sprintf(
					"handoff %s was already delivered but is still queued in the %s outbox",
					short(o.ID), o.Role),
				Repairable: true,
				Detail:     map[string]string{"id": o.ID, "path": o.Path},
			})
		}
	}

	if files, err := s.StaleTempFiles(); err == nil && len(files) > 0 {
		add(Diagnostic{
			Code: CodeTempFiles, Severity: SeverityInfo, Component: "runtime",
			Message:    fmt.Sprintf("%d stale temporary file(s) left by an interrupted write", len(files)),
			Repairable: true,
			Detail:     map[string]string{"count": fmt.Sprint(len(files))},
		})
	}

	if held, err := s.LifecycleLockHeld(); err == nil && held {
		add(Diagnostic{
			Code: CodeLockActive, Severity: SeverityInfo, Component: "runtime",
			Message: "a lifecycle operation is running in another terminal",
		})
	}

	sortDiagnostics(diags)

	report.Diagnostics = diags
	report.Running = anySession || daemonState == "running"
	report.Health = health(report.Running, diags)

	return report
}

// inspectWorktree classifies one role's worktree.
func inspectWorktree(s System, role Role) []Diagnostic {
	wt, err := s.InspectWorktree(role.Name)
	if err != nil {
		return []Diagnostic{{
			Code: CodeWorktreeInvalid, Severity: SeverityError, Component: role.Name,
			Message: fmt.Sprintf("cannot inspect the worktree: %v", err),
		}}
	}

	detail := map[string]string{
		"path":            wt.Path,
		"expected_branch": wt.ExpectedBranch,
		"actual_branch":   wt.ActualBranch,
	}

	switch wt.State {
	case WorktreeHealthy:
		return nil

	case WorktreeMissing:
		// Safe to recreate only when Git holds no conflicting registration.
		return []Diagnostic{{
			Code: CodeWorktreeMissing, Severity: SeverityError, Component: role.Name,
			Message:    fmt.Sprintf("worktree %s is missing", wt.Path),
			Repairable: !wt.Registered,
			Detail:     detail,
		}}

	case WorktreeDirty:
		// Never repairable: repairing would mean discarding someone's work.
		return []Diagnostic{{
			Code: CodeWorktreeDirty, Severity: SeverityWarning, Component: role.Name,
			Message: fmt.Sprintf(
				"worktree %s has uncommitted changes; commit or stash them yourself — "+
					"swarm will not touch them", wt.Path),
			Repairable: false,
			Detail:     detail,
		}}

	case WorktreeDetached:
		return []Diagnostic{{
			Code: CodeWorktreeDetached, Severity: SeverityError, Component: role.Name,
			Message: fmt.Sprintf("worktree %s is on a detached HEAD, not %s",
				wt.Path, wt.ExpectedBranch),
			Repairable: false,
			Detail:     detail,
		}}

	case WorktreeWrongRef:
		return []Diagnostic{{
			Code: CodeWorktreeBranch, Severity: SeverityError, Component: role.Name,
			Message: fmt.Sprintf("worktree %s is on %s, expected %s",
				wt.Path, wt.ActualBranch, wt.ExpectedBranch),
			Repairable: false,
			Detail:     detail,
		}}

	default:
		return []Diagnostic{{
			Code: CodeWorktreeInvalid, Severity: SeverityError, Component: role.Name,
			Message:    fmt.Sprintf("worktree %s has inconsistent Git metadata", wt.Path),
			Repairable: false,
			Detail:     detail,
		}}
	}
}

// inspectRuntime classifies a role's session and agent.
func inspectRuntime(s System, role Role, serverAlive, sessionLive bool) []Diagnostic {
	if !serverAlive {
		// With no server at all, per-role session diagnostics would just be
		// noise: the swarm is simply not running.
		return nil
	}

	if !sessionLive {
		return []Diagnostic{{
			Code: CodeSessionMissing, Severity: SeverityError, Component: role.Name,
			Message:    "tmux session is missing while the rest of the swarm is running",
			Repairable: true,
		}}
	}

	state, err := s.AgentState(role.Name)
	if err != nil {
		return nil
	}

	if state == "not-started" {
		return []Diagnostic{{
			Code: CodeAgentMissing, Severity: SeverityError, Component: role.Name,
			Message:    "the session is running but its agent has exited",
			Repairable: true,
		}}
	}

	return nil
}

// inspectWork validates the durable lifecycle invariants for a role.
func inspectWork(s System, role Role) []Diagnostic {
	var out []Diagnostic

	current, err := s.CurrentCount(role.Name)
	if err == nil && role.ReceiveMode == "task" && current > 1 {
		// Task mode means exactly one item at a time. More than one is
		// corruption, and choosing a winner silently could lose work.
		out = append(out, Diagnostic{
			Code: CodeCurrentCorrupt, Severity: SeverityCritical, Component: role.Name,
			Message: fmt.Sprintf(
				"%d items are in current/ but %s receives one task at a time; "+
					"move the extras back to inbox/ yourself", current, role.Name),
			Repairable: false,
			Detail:     map[string]string{"count": fmt.Sprint(current)},
		})
	}

	if n, err := s.FailedCount(role.Name); err == nil && n > 0 {
		out = append(out, Diagnostic{
			Code: CodeHandoffFailed, Severity: SeverityWarning, Component: role.Name,
			Message: fmt.Sprintf("%d handoff(s) failed to send; see `swarm handoff retry`", n),
			Detail:  map[string]string{"count": fmt.Sprint(n)},
		})
	}

	return out
}

// sortDiagnostics puts the most serious first, then groups by component.
func sortDiagnostics(diags []Diagnostic) {
	rank := map[Severity]int{
		SeverityCritical: 0,
		SeverityError:    1,
		SeverityWarning:  2,
		SeverityInfo:     3,
	}

	sort.SliceStable(diags, func(i, j int) bool {
		if rank[diags[i].Severity] != rank[diags[j].Severity] {
			return rank[diags[i].Severity] < rank[diags[j].Severity]
		}
		if diags[i].Component != diags[j].Component {
			return diags[i].Component < diags[j].Component
		}
		return diags[i].Code < diags[j].Code
	})
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
