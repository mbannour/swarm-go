package handoff

import (
	"sort"
	"strings"
)

// TraceEvent is one durable handoff observed somewhere in the tree, together
// with where it currently sits.
type TraceEvent struct {
	Entry
	Owner string // the role whose box holds it
	Box   string // inbox, current, completed, outbox, sent, failed
}

// Trace reconstructs the history of a task from durable state alone.
//
// Nothing is recorded specially for tracing: every handoff already carries an
// id, a source id, timestamps and a task name, so the chain can be rebuilt by
// reading the boxes. Deliveries and their source copies share an id, so each
// logical message is reported once, at the furthest point it reached.
func (s *Store) Trace(task string) ([]TraceEvent, error) {
	// Later boxes win when the same id appears in several places, so a message
	// is reported where it ended up rather than where it started.
	boxRank := map[string]int{
		BoxOutbox:    0,
		BoxSent:      1,
		BoxFailed:    2,
		BoxInbox:     3,
		BoxCurrent:   4,
		BoxCompleted: 5,
	}

	best := map[string]TraceEvent{}

	for role := range s.Roles {
		for _, box := range roleBoxes {
			entries, err := s.List(role, box)
			if err != nil {
				return nil, err
			}

			for _, e := range entries {
				if task != "" && !matchesTask(e, task) {
					continue
				}

				candidate := TraceEvent{Entry: e, Owner: role, Box: box}

				existing, seen := best[e.ID]
				if !seen || boxRank[box] > boxRank[existing.Box] {
					best[e.ID] = candidate
				}
			}
		}
	}

	events := make([]TraceEvent, 0, len(best))
	for _, e := range best {
		events = append(events, e)
	}

	// Chronological by creation, then by id so the order is total.
	sort.Slice(events, func(i, j int) bool {
		if !events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].CreatedAt.Before(events[j].CreatedAt)
		}
		return events[i].ID < events[j].ID
	})

	return events, nil
}

// matchesTask reports whether an entry belongs to a task. A note produced from
// a task carries no task field of its own, so the note text is also consulted.
func matchesTask(e Entry, task string) bool {
	if e.Task == task {
		return true
	}
	return strings.Contains(e.Note, task)
}

// TraceChain links events by source id, so a caller can follow the flow.
// The result maps a handoff id to the handoffs produced from it.
func TraceChain(events []TraceEvent) map[string][]TraceEvent {
	children := map[string][]TraceEvent{}

	for _, e := range events {
		if e.SourceID != "" {
			children[e.SourceID] = append(children[e.SourceID], e)
		}
	}

	return children
}
