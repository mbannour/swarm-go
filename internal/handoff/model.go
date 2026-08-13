// Package handoff implements durable role-to-role work: a small text format,
// its validation rules, the on-disk lifecycle, a daemon that delivers messages,
// and the receive-side ready/done state machine.
//
// It knows nothing about tmux or any agent backend. Waking a destination agent
// is delegated to a Notifier, and resolving Git commits to a CommitResolver;
// both are supplied by the caller.
package handoff

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

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

// ReceiveMode decides how a role picks work off its inbox.
type ReceiveMode string

const (
	// ModeTask takes exactly one message at a time.
	ModeTask ReceiveMode = "task"
	// ModeBatch takes every message sharing the highest available priority.
	ModeBatch ReceiveMode = "batch"
)

// Priority bounds. Higher means more urgent.
const (
	MinPriority = 0
	MaxPriority = 100
)

// Handoff is one logical message from one role to one or more roles.
//
// ID, CreatedAt, DeliveredAt and CanonicalCommit are lifecycle metadata: the
// application generates them, a sender never supplies them.
type Handoff struct {
	ID              string    // generated, unique per logical handoff
	Type            Type      //
	From            string    //
	To              []string  // one or more configured roles
	Task            string    // git_handoff only
	Commit          string    // git_handoff only, exactly 10 hex characters
	CanonicalCommit string    // generated: full SHA resolved by the daemon
	Priority        int       //
	CreatedAt       time.Time // generated at send time
	DeliveredAt     time.Time // generated at delivery time, per destination
	Note            string    //
}

// Entry is a handoff together with the file it was read from.
type Entry struct {
	Handoff
	Name string // file name only
	Path string // absolute path
}

// FileExt is the extension of a handoff file.
const FileExt = ".handoff"

// timeFormat is the sortable, filesystem-safe stamp used in file names.
const timeFormat = "20060102T150405.000000000"

// WakeUpMessage is the fixed text sent to a destination agent. It is a
// constant on purpose: no handoff content is ever interpolated into it.
const WakeUpMessage = "A new handoff is available in your inbox. Inspect it before continuing."

// Notifier wakes the agent of a role after a delivery.
type Notifier interface {
	Notify(role string) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(role string) error

func (f NotifierFunc) Notify(role string) error { return f(role) }

// Clock returns the current time; tests substitute a deterministic one.
type Clock func() time.Time

// idBytes is the entropy behind a handoff ID: 128 bits, so IDs stay unique
// without depending on the clock.
const idBytes = 16

// NewID returns a fresh random identifier. Delivery file names derive from it,
// which is what makes a repeated delivery detectable instead of duplicated.
func NewID() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
