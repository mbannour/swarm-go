package handoff

// Selection is the work a role takes on in one ready call.
type Selection struct {
	Mode    ReceiveMode
	Entries []Entry
	// Priority is the priority shared by every selected entry.
	Priority int
}

// Empty reports whether nothing was selected.
func (s Selection) Empty() bool { return len(s.Entries) == 0 }

// Select picks work from candidates according to a role's receive mode.
//
//   - ModeTask  takes exactly one entry: highest priority, then oldest, then
//     the deterministic file-name tie-breaker.
//   - ModeBatch takes every entry sharing the highest available priority, in
//     the same order. Lower-priority work is left for a later batch.
//
// Candidates need not be sorted; Select sorts a copy.
func Select(candidates []Entry, mode ReceiveMode) Selection {
	if len(candidates) == 0 {
		return Selection{Mode: mode}
	}

	ordered := make([]Entry, len(candidates))
	copy(ordered, candidates)
	SortEntries(ordered)

	top := ordered[0].Priority

	switch mode {
	case ModeBatch:
		var batch []Entry
		for _, e := range ordered {
			if e.Priority != top {
				break // sorted descending: nothing below can match
			}
			batch = append(batch, e)
		}
		return Selection{Mode: ModeBatch, Entries: batch, Priority: top}

	default: // ModeTask is the safe default for an unknown mode
		return Selection{Mode: ModeTask, Entries: ordered[:1], Priority: top}
	}
}
