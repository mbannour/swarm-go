// Package lifecycle composes the independent components — worktrees, tmux
// sessions, agents and the handoff daemon — into one reliable start/status/stop
// for a repository.
//
// It coordinates; it does not reimplement. Every component is reached through a
// small interface so the orchestration can be tested without tmux or an AI
// backend.
package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RuntimeDir is the repository-relative home of managed runtime state.
const RuntimeDir = ".swarm/runtime"

// Runtime file names.
const (
	daemonPIDFile   = "handoffd.pid"
	daemonLockFile  = "handoffd.lock"
	lifecycleLock   = "lifecycle.lock"
	stateFile       = "state.json"
	logsDir         = "logs"
	daemonLogName   = "handoffd.log"
	daemonLogTarget = logsDir + "/" + daemonLogName
)

// ComponentState is the observed state of one runtime component.
type ComponentState string

const (
	StateRunning ComponentState = "running"
	StateStopped ComponentState = "stopped"
	StateMissing ComponentState = "missing"
	StateFailed  ComponentState = "failed"
	StateUnknown ComponentState = "unknown"
)

// Health is the overall verdict for a swarm.
type Health string

const (
	HealthStopped  Health = "stopped"
	HealthHealthy  Health = "healthy"
	HealthDegraded Health = "degraded"
	HealthFailed   Health = "failed"
)

// Role is the lifecycle's view of one configured role.
type Role struct {
	Name         string
	Backend      string
	WorktreeName string // e.g. wt-coder
	Worktree     string // absolute path
	Branch       string
	ReceiveMode  string
	Approval     string // interactive | autonomous | restricted
}

// NotificationStatus is what status reports about waking a role.
type NotificationStatus struct {
	Status   string `json:"status"`             // pending | sent | failed | not-required
	Attempts int    `json:"attempts,omitempty"` //
	Error    string `json:"error,omitempty"`    //
}

// RoleStatus is what status reports for one role.
//
// Session, Agent, Work and Notification are deliberately separate: a session
// can exist with no agent, an agent can be running with nothing to do, and work
// can be waiting for a role that was never successfully told about it.
type RoleStatus struct {
	Role         string             `json:"name"`
	Worktree     ComponentState     `json:"worktree"`
	Session      ComponentState     `json:"session"`
	Agent        ComponentState     `json:"agent"`
	Work         string             `json:"work"`           // waiting | ready | working
	Task         string             `json:"task,omitempty"` // the current task, if any
	Notification NotificationStatus `json:"notification"`
}

// Counts summarises durable handoff state across all roles.
type Counts struct {
	Inbox     int `json:"inbox"`
	Current   int `json:"current"`
	Outbox    int `json:"outbox"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Rejected  int `json:"rejected"`
}

// DaemonStatus describes the handoff daemon.
type DaemonStatus struct {
	State ComponentState `json:"status"`
	PID   int            `json:"pid,omitempty"`
	Log   string         `json:"log,omitempty"`
}

// SwarmStatus is the whole picture, and the shape of `swarm status --json`.
type SwarmStatus struct {
	Repository string       `json:"repository"`
	Health     Health       `json:"health"`
	Daemon     DaemonStatus `json:"daemon"`
	Roles      []RoleStatus `json:"roles"`
	Handoffs   Counts       `json:"handoffs"`
	StartedAt  string       `json:"started_at,omitempty"`
}

// Running reports whether any managed component is up.
func (s SwarmStatus) Running() bool {
	if s.Daemon.State == StateRunning {
		return true
	}
	for _, r := range s.Roles {
		if r.Session == StateRunning || r.Agent == StateRunning {
			return true
		}
	}
	return false
}

// JSON renders the status as indented JSON.
func (s SwarmStatus) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// Metadata is the small amount of information worth persisting between runs.
//
// It is metadata, never truth: whether a session or process is alive is always
// answered by asking tmux or the operating system.
type Metadata struct {
	Repository string    `json:"repository"`
	StartedAt  time.Time `json:"started_at"`
	Roles      []string  `json:"roles"`
}

// runtimePath resolves a file inside the runtime directory.
func runtimePath(repoRoot, name string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(RuntimeDir), filepath.FromSlash(name))
}

// EnsureRuntimeDir creates the runtime directory tree.
func EnsureRuntimeDir(repoRoot string) error {
	return os.MkdirAll(runtimePath(repoRoot, logsDir), 0o755)
}

// DaemonLogPath is where the managed daemon's output is captured.
func DaemonLogPath(repoRoot string) string {
	return runtimePath(repoRoot, daemonLogTarget)
}

// RoleLogPath is where a role's managed log would live. Agents run
// interactively inside tmux, so this is only used by commands that write one.
func RoleLogPath(repoRoot, role string) string {
	return runtimePath(repoRoot, logsDir+"/"+role+".log")
}

// writeMetadata records the last successful start.
func writeMetadata(repoRoot string, m Metadata) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := EnsureRuntimeDir(repoRoot); err != nil {
		return err
	}
	return os.WriteFile(runtimePath(repoRoot, stateFile), append(data, '\n'), 0o644)
}

// readMetadata loads the persisted metadata, if any.
func readMetadata(repoRoot string) (Metadata, bool) {
	data, err := os.ReadFile(runtimePath(repoRoot, stateFile))
	if err != nil {
		return Metadata{}, false
	}

	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return Metadata{}, false
	}

	return m, true
}
