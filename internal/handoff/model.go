// Package handoff implements durable role-to-role messages: a small text
// format, its validation rules, the on-disk inbox/outbox layout, and a daemon
// that moves messages from outboxes to inboxes.
//
// It knows nothing about tmux or any agent backend; waking a destination agent
// is delegated to a Notifier supplied by the caller.
package handoff

import "time"

// Type is the kind of a handoff message.
type Type string

const (
	// TypeGit transfers implementation work identified by a commit.
	TypeGit Type = "git_handoff"
	// TypeNote is lightweight coordination between roles.
	TypeNote Type = "note"
)

// Types lists every supported message type.
func Types() []Type { return []Type{TypeGit, TypeNote} }

// Priority bounds. Higher means more urgent.
const (
	MinPriority = 0
	MaxPriority = 100
)

// Handoff is one message from one role to another.
type Handoff struct {
	Type     Type
	From     string
	To       string
	Task     string // required for TypeGit
	Commit   string // required for TypeGit
	Priority int
	Note     string
}

// Entry is a handoff together with the file it was read from.
type Entry struct {
	Handoff
	Name string // file name only, e.g. 20260813T210501...-coder-to-refactorer.handoff
	Path string // absolute path
}

// FileExt is the extension of a handoff file.
const FileExt = ".handoff"

// timeFormat is the sortable, filesystem-safe stamp used in file names. It
// carries nanoseconds so two handoffs in the same second cannot collide.
const timeFormat = "20060102T150405.000000000"

// WakeUpMessage is the fixed text sent to a destination agent. It is a
// constant on purpose: no handoff content is ever interpolated into it.
const WakeUpMessage = "A new handoff is available in your inbox. Inspect it before continuing."

// Notifier wakes the agent of a role after a delivery. Implementations live
// outside this package (the CLI supplies a tmux-backed one).
type Notifier interface {
	Notify(role string) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(role string) error

func (f NotifierFunc) Notify(role string) error { return f(role) }

// Clock returns the current time; tests substitute a deterministic one.
type Clock func() time.Time
