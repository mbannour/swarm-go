package handoff

import (
	"fmt"
)

// Lifecycle drives the receive side: accepting work (ready) and finishing it
// (done). All of its state is on disk, so a crash between any two steps leaves
// a consistent picture that the next command can recover from.
type Lifecycle struct {
	Store *Store
	// Mode resolves a role's receive mode from configuration.
	Mode func(role string) (ReceiveMode, error)
}

// NewLifecycle wires a lifecycle to a store and a mode lookup.
func NewLifecycle(store *Store, mode func(role string) (ReceiveMode, error)) *Lifecycle {
	return &Lifecycle{Store: store, Mode: mode}
}

// Ready returns the role's current work, accepting new work if it has none.
//
// The important property: if current/ is already occupied, Ready returns
// exactly that and selects nothing new. A task can therefore never be accepted
// twice, and a crash after inbox → current is recovered on the next call.
func (l *Lifecycle) Ready(role string) (Selection, error) {
	mode, err := l.Mode(role)
	if err != nil {
		return Selection{}, err
	}

	// Already working on something? Hand it back unchanged.
	current, err := l.Store.Current(role)
	if err != nil {
		return Selection{}, err
	}
	if len(current) > 0 {
		SortEntries(current)
		return Selection{Mode: mode, Entries: current, Priority: current[0].Priority}, nil
	}

	// A stale marker without files means an interrupted batch completion.
	if err := l.Store.ClearBatchID(role); err != nil {
		return Selection{}, err
	}

	inbox, err := l.Store.Inbox(role)
	if err != nil {
		return Selection{}, err
	}

	selection := Select(inbox, mode)
	if selection.Empty() {
		return selection, nil
	}

	// Move each selected item into current/. A partial move is safe: whatever
	// arrived is current work, and the rest stays in the inbox for later.
	moved := make([]Entry, 0, len(selection.Entries))
	for _, e := range selection.Entries {
		dst, err := l.Store.MoveTo(e.Path, role, BoxCurrent)
		if err != nil {
			if len(moved) == 0 {
				return Selection{}, fmt.Errorf("accept %s: %w", e.Name, err)
			}
			break
		}
		e.Path = dst
		moved = append(moved, e)
	}

	if selection.Mode == ModeBatch {
		id, err := NewID()
		if err != nil {
			return Selection{}, err
		}
		if err := l.Store.SetBatchID(role, id); err != nil {
			return Selection{}, err
		}
	}

	selection.Entries = moved

	return selection, nil
}

// SourceID identifies the work a role is currently doing. For a batch it is
// the first item's id, which is stable because the batch is ordered.
func (l *Lifecycle) SourceID(role string) (string, error) {
	current, err := l.Store.Current(role)
	if err != nil {
		return "", err
	}
	if len(current) == 0 {
		return "", nil
	}

	SortEntries(current)

	return current[0].ID, nil
}

// Advance creates the downstream handoff for a role's current work.
//
// It is idempotent per piece of current work: if this role already produced a
// message from the same source, that message is returned and nothing new is
// created. That is what makes a crash between "send" and "done" safe — the
// agent re-runs the same command after restart and gets the original handoff
// back instead of a duplicate.
func (l *Lifecycle) Advance(role string, h Handoff) (entry Entry, already bool, err error) {
	sourceID, err := l.SourceID(role)
	if err != nil {
		return Entry{}, false, err
	}
	if sourceID == "" {
		return Entry{}, false, fmt.Errorf("role %q has no current work to hand off", role)
	}

	existing, err := l.Store.FindBySource(role, sourceID)
	if err != nil {
		return Entry{}, false, err
	}
	if len(existing) > 0 {
		return existing[0], true, nil
	}

	h.From = role
	h.SourceID = sourceID

	entry, err = l.Store.Send(h)
	if err != nil {
		return Entry{}, false, err
	}

	return entry, false, nil
}

// Status is a read-only snapshot of one role's work state.
type Status struct {
	Role           string
	Mode           ReceiveMode
	Current        []Entry
	Inbox          int
	Downstream     []Entry // messages already produced from the current work
	DownstreamSent bool
}

// State is the coarse work state used by `agents list` and `status`.
func (s Status) State() string {
	switch {
	case len(s.Current) > 0:
		return "working"
	case s.Inbox > 0:
		return "ready"
	default:
		return "waiting"
	}
}

// Status inspects a role without changing anything.
func (l *Lifecycle) Status(role string) (Status, error) {
	mode, err := l.Mode(role)
	if err != nil {
		return Status{}, err
	}

	current, err := l.Store.Current(role)
	if err != nil {
		return Status{}, err
	}
	SortEntries(current)

	inbox, err := l.Store.Inbox(role)
	if err != nil {
		return Status{}, err
	}

	status := Status{Role: role, Mode: mode, Current: current, Inbox: len(inbox)}

	if len(current) > 0 {
		downstream, err := l.Store.FindBySource(role, current[0].ID)
		if err != nil {
			return Status{}, err
		}
		status.Downstream = downstream
		status.DownstreamSent = len(downstream) > 0
	}

	return status, nil
}

// Done moves everything in current/ to completed/ and then looks for the next
// available work, so an agent can loop on `done` alone.
func (l *Lifecycle) Done(role string) (finished []Entry, next Selection, err error) {
	current, err := l.Store.Current(role)
	if err != nil {
		return nil, Selection{}, err
	}

	for _, e := range current {
		dst, err := l.Store.MoveTo(e.Path, role, BoxCompleted)
		if err != nil {
			return finished, Selection{}, fmt.Errorf("complete %s: %w", e.Name, err)
		}
		e.Path = dst
		finished = append(finished, e)
	}

	if err := l.Store.ClearBatchID(role); err != nil {
		return finished, Selection{}, err
	}

	// current/ is empty now, so this selects fresh work rather than replaying.
	next, err = l.Ready(role)
	if err != nil {
		return finished, Selection{}, err
	}

	return finished, next, nil
}
