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
