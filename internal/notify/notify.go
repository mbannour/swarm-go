// Package notify owns waking agents and remembering whether it worked.
//
// Delivery and notification are separate events. A handoff in an inbox is
// durable and authoritative; a wake-up is a best-effort hint that something
// arrived. Keeping the two apart is what lets a failed notification be retried
// without ever touching the delivered message.
package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status is the outcome of trying to wake a role.
type Status string

const (
	// StatusPending means work is waiting and no wake-up has succeeded yet.
	StatusPending Status = "pending"
	// StatusSent means a wake-up was delivered to the agent.
	StatusSent Status = "sent"
	// StatusFailed means every attempt so far failed.
	StatusFailed Status = "failed"
	// StatusNotRequired means there is nothing to wake anyone about.
	StatusNotRequired Status = "not-required"
)

// Dir is the repository-relative home of notification state.
const Dir = ".swarm/runtime/notifications"

// Defaults for reconciliation. A local interactive agent needs seconds, not
// milliseconds: re-notifying faster would flood the pane without helping.
const (
	DefaultRetryAfter  = 15 * time.Second
	DefaultMaxAttempts = 5
)

// State is what is remembered about waking one role.
type State struct {
	Role string `json:"role"`
	// HandoffID is the message the last attempt was about. A new message
	// resets the attempt count: a fresh arrival deserves a fresh try.
	HandoffID     string    `json:"handoff_id,omitempty"`
	Status        Status    `json:"status"`
	Attempts      int       `json:"attempts"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

// Exhausted reports whether retrying is pointless.
func (s State) Exhausted(max int) bool {
	return s.Status == StatusFailed && s.Attempts >= max
}

// Notifier wakes one role's agent. Implementations are backend-specific: how
// you get an interactive TUI's attention is not the daemon's business.
type Notifier interface {
	Notify(role string) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(role string) error

func (f NotifierFunc) Notify(role string) error { return f(role) }

// Clock returns the current time; tests substitute a deterministic one.
type Clock func() time.Time

// Tracker wakes roles and records what happened, durably.
//
// It is the single path from "this role has work it does not know about" to
// "the agent has been told" — used by daemon delivery, task submission and
// reconciliation alike, so notification behavior cannot drift between them.
type Tracker struct {
	Root     string // repository root
	Notifier Notifier
	Now      Clock

	RetryAfter  time.Duration
	MaxAttempts int

	mu sync.Mutex
}

// NewTracker returns a tracker with sane defaults.
func NewTracker(root string, n Notifier) *Tracker {
	return &Tracker{
		Root: root, Notifier: n, Now: time.Now,
		RetryAfter: DefaultRetryAfter, MaxAttempts: DefaultMaxAttempts,
	}
}

// statePath is where a role's notification state lives.
func (t *Tracker) statePath(role string) (string, error) {
	if role == "" || role == "." || role == ".." || strings.ContainsAny(role, `/\`) {
		return "", fmt.Errorf("invalid role name %q", role)
	}
	return filepath.Join(t.Root, filepath.FromSlash(Dir), role+".json"), nil
}

// State returns what is known about waking a role.
func (t *Tracker) State(role string) State {
	path, err := t.statePath(role)
	if err != nil {
		return State{Role: role, Status: StatusNotRequired}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return State{Role: role, Status: StatusNotRequired}
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{Role: role, Status: StatusNotRequired}
	}
	s.Role = role

	return s
}

// save persists a role's state.
func (t *Tracker) save(s State) error {
	path, err := t.statePath(s.Role)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Clear forgets a role's notification state, which is what happens once the
// role actually accepts work: the wake-up did its job.
func (t *Tracker) Clear(role string) error {
	path, err := t.statePath(role)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// Notify wakes a role about a specific handoff and records the outcome.
//
// The returned error is informational: the caller must not roll anything back
// because of it. The message is already durable, and the failure is remembered
// so reconciliation can try again.
func (t *Tracker) Notify(role, handoffID string) error {
	_, err := t.NotifyAndRecord(role, handoffID)
	return err
}

// NotifyAndRecord is Notify, returning the resulting state as well.
func (t *Tracker) NotifyAndRecord(role, handoffID string) (State, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.State(role)

	// A different message means a fresh start: previous failures were about
	// something else.
	if state.HandoffID != handoffID {
		state = State{Role: role, HandoffID: handoffID}
	}

	state.Attempts++
	state.LastAttemptAt = t.now()

	var err error
	if t.Notifier != nil {
		err = t.Notifier.Notify(role)
	}

	if err != nil {
		state.Status = StatusFailed
		state.LastError = err.Error()
	} else {
		state.Status = StatusSent
		state.LastError = ""
	}

	if saveErr := t.save(state); saveErr != nil && err == nil {
		err = saveErr
	}

	return state, err
}

// ShouldRetry reports whether a role is worth re-notifying now.
//
// It is deliberately conservative: only when the last attempt is old enough,
// and never past the attempt limit, so a wedged agent is not flooded.
func (t *Tracker) ShouldRetry(role, handoffID string) bool {
	state := t.State(role)

	// Nothing tried yet for this message: notify.
	if state.HandoffID != handoffID || state.Attempts == 0 {
		return true
	}
	if state.Exhausted(t.maxAttempts()) {
		return false
	}
	// A successful wake-up that the agent ignored is still worth repeating
	// once the interval has passed — the agent may have been busy.
	return t.now().Sub(state.LastAttemptAt) >= t.retryAfter()
}

func (t *Tracker) now() time.Time {
	if t.Now == nil {
		return time.Now()
	}
	return t.Now()
}

func (t *Tracker) retryAfter() time.Duration {
	if t.RetryAfter <= 0 {
		return DefaultRetryAfter
	}
	return t.RetryAfter
}

func (t *Tracker) maxAttempts() int {
	if t.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return t.MaxAttempts
}
