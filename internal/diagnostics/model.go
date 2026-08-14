// Package diagnostics inspects a swarm and reports what is wrong with it.
//
// It only observes: nothing here starts, stops, creates or deletes anything.
// Turning diagnostics into changes is the repair package's job, and it consumes
// these typed values — never formatted output.
package diagnostics

import "encoding/json"

// Severity is how much a diagnostic matters.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Stable diagnostic codes. Automation matches on these, never on messages.
const (
	CodeRepoMissing      = "REPO_MISSING"
	CodeRepoNoCommits    = "REPO_NO_COMMITS"
	CodeConfigInvalid    = "CONFIG_INVALID"
	CodePromptsMissing   = "PROMPTS_MISSING"
	CodeBackendMissing   = "BACKEND_MISSING"
	CodeRuntimeNotWrite  = "RUNTIME_NOT_WRITABLE"
	CodeWorktreeMissing  = "WORKTREE_MISSING"
	CodeWorktreeDirty    = "WORKTREE_DIRTY"
	CodeWorktreeBranch   = "WORKTREE_WRONG_BRANCH"
	CodeWorktreeDetached = "WORKTREE_DETACHED"
	CodeWorktreeInvalid  = "WORKTREE_INVALID_METADATA"
	CodeWorktreeStale    = "WORKTREE_STALE_METADATA"
	CodeTmuxMissing      = "TMUX_MISSING"
	CodeSocketStale      = "TMUX_SOCKET_STALE"
	CodeSessionMissing   = "SESSION_MISSING"
	CodeAgentMissing     = "AGENT_MISSING"
	CodeDaemonNotRunning = "DAEMON_NOT_RUNNING"
	CodeDaemonStalePID   = "DAEMON_STALE_PID"
	CodeDaemonUnverified = "DAEMON_UNVERIFIED"
	CodeLockActive       = "LOCK_ACTIVE"
	CodeLockStale        = "LOCK_STALE_METADATA"
	CodeHandoffFailed    = "HANDOFF_FAILED"
	CodeHandoffRejected  = "HANDOFF_REJECTED"
	CodeCurrentCorrupt   = "HANDOFF_CURRENT_CORRUPT"
	CodeOrphanDelivery   = "HANDOFF_ORPHAN_DELIVERY"
	CodeNotifyFailed     = "DELIVERY_NOTIFICATION_FAILED"
	CodeTempFiles        = "RUNTIME_TEMP_FILES"
)

// Diagnostic is one observation about the system.
type Diagnostic struct {
	Code      string   `json:"code"`
	Severity  Severity `json:"severity"`
	Component string   `json:"component"`
	Message   string   `json:"message"`

	// Repairable marks issues `swarm repair` can fix safely and
	// deterministically. Anything ambiguous or destructive is false.
	Repairable bool `json:"repairable"`

	// Detail carries structured context (a path, an id, a branch name) so
	// tooling does not have to parse Message.
	Detail map[string]string `json:"detail,omitempty"`
}

// Health is the overall verdict.
type Health string

const (
	// HealthHealthy means every required component is functioning.
	HealthHealthy Health = "healthy"
	// HealthDegraded means something is wrong but recoverable.
	HealthDegraded Health = "degraded"
	// HealthBlocked means the state is unsafe or ambiguous: a human must look.
	HealthBlocked Health = "blocked"
	// HealthStopped means the swarm is intentionally not running.
	HealthStopped Health = "stopped"
)

// Report is the whole diagnosis, and the shape of `swarm doctor --json`.
type Report struct {
	Repository  string       `json:"repository"`
	Health      Health       `json:"health"`
	Running     bool         `json:"running"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// JSON renders the report as indented JSON.
func (r Report) JSON() ([]byte, error) {
	if r.Diagnostics == nil {
		r.Diagnostics = []Diagnostic{}
	}
	return json.MarshalIndent(r, "", "  ")
}

// Repairable returns the diagnostics repair can act on.
func (r Report) Repairable() []Diagnostic {
	var out []Diagnostic
	for _, d := range r.Diagnostics {
		if d.Repairable {
			out = append(out, d)
		}
	}
	return out
}

// Blocking returns the diagnostics that need a human.
func (r Report) Blocking() []Diagnostic {
	var out []Diagnostic
	for _, d := range r.Diagnostics {
		if !d.Repairable && (d.Severity == SeverityError || d.Severity == SeverityCritical) {
			out = append(out, d)
		}
	}
	return out
}

// ExitCode is the documented exit status for `swarm doctor`:
//
//	0  healthy, or intentionally stopped
//	1  degraded — recoverable, try `swarm repair`
//	2  blocked  — a human must intervene
func (r Report) ExitCode() int {
	switch r.Health {
	case HealthBlocked:
		return 2
	case HealthDegraded:
		return 1
	default:
		return 0
	}
}

// health derives the overall verdict from the diagnostics.
func health(running bool, diags []Diagnostic) Health {
	blocked := false
	degraded := false

	for _, d := range diags {
		switch d.Severity {
		case SeverityCritical:
			blocked = true
		case SeverityError:
			if d.Repairable {
				degraded = true
			} else {
				blocked = true
			}
		case SeverityWarning:
			degraded = true
		}
	}

	switch {
	case blocked:
		return HealthBlocked
	case !running:
		// A stopped swarm is not unhealthy, but a warning still matters.
		if degraded {
			return HealthDegraded
		}
		return HealthStopped
	case degraded:
		return HealthDegraded
	default:
		return HealthHealthy
	}
}
