package handoff

import "testing"

// entries builds candidates whose names encode arrival order.
func entries(specs ...struct {
	name     string
	priority int
}) []Entry {
	out := make([]Entry, 0, len(specs))
	for _, s := range specs {
		out = append(out, Entry{
			Handoff: Handoff{Priority: s.priority, Note: s.name},
			Name:    s.name,
			Path:    "/tmp/" + s.name,
		})
	}
	return out
}

type spec = struct {
	name     string
	priority int
}

func TestSelectTaskTakesHighestPriority(t *testing.T) {
	got := Select(entries(
		spec{"c-low", 10},
		spec{"a-high", 30},
		spec{"b-mid", 20},
	), ModeTask)

	if len(got.Entries) != 1 {
		t.Fatalf("task mode selected %d entries", len(got.Entries))
	}
	if got.Entries[0].Name != "a-high" || got.Priority != 30 {
		t.Errorf("selected %+v", got.Entries[0])
	}
}

func TestSelectTaskBreaksTiesOldestFirst(t *testing.T) {
	// Names begin with a timestamp, so lexical order is arrival order.
	got := Select(entries(
		spec{"20260813T120002.000000000-b", 20},
		spec{"20260813T120001.000000000-a", 20},
		spec{"20260813T120003.000000000-c", 20},
	), ModeTask)

	if got.Entries[0].Name != "20260813T120001.000000000-a" {
		t.Errorf("selected %q, want the oldest", got.Entries[0].Name)
	}
}

func TestSelectBatchTakesEveryTopPriorityItem(t *testing.T) {
	got := Select(entries(
		spec{"a", 20},
		spec{"b", 20},
		spec{"c", 10},
		spec{"d", 5},
	), ModeBatch)

	if len(got.Entries) != 2 {
		t.Fatalf("batch selected %d entries, want 2", len(got.Entries))
	}
	if got.Priority != 20 {
		t.Errorf("batch priority = %d, want 20", got.Priority)
	}
	for _, e := range got.Entries {
		if e.Priority != 20 {
			t.Errorf("batch included priority %d", e.Priority)
		}
	}
}

func TestSelectBatchOrdersOldestFirst(t *testing.T) {
	got := Select(entries(
		spec{"20260813T120002.000000000-b", 30},
		spec{"20260813T120001.000000000-a", 30},
	), ModeBatch)

	if got.Entries[0].Name != "20260813T120001.000000000-a" {
		t.Errorf("batch order = %q first", got.Entries[0].Name)
	}
}

func TestSelectEmpty(t *testing.T) {
	for _, mode := range []ReceiveMode{ModeTask, ModeBatch} {
		got := Select(nil, mode)
		if !got.Empty() {
			t.Errorf("%s: selected %+v from nothing", mode, got.Entries)
		}
	}
}

func TestSelectUnknownModeFallsBackToTask(t *testing.T) {
	got := Select(entries(spec{"a", 20}, spec{"b", 20}), ReceiveMode("nonsense"))

	if len(got.Entries) != 1 {
		t.Errorf("unknown mode selected %d entries, want 1", len(got.Entries))
	}
}

func TestSelectDoesNotMutateInput(t *testing.T) {
	in := entries(spec{"low", 1}, spec{"high", 90})

	Select(in, ModeTask)

	if in[0].Name != "low" {
		t.Error("Select reordered its input")
	}
}
