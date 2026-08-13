package handoff

import (
	"testing"
	"time"
)

// modes maps roles to receive modes for tests.
func modes(m map[string]ReceiveMode) func(string) (ReceiveMode, error) {
	return func(role string) (ReceiveMode, error) {
		if mode, ok := m[role]; ok {
			return mode, nil
		}
		return ModeTask, nil
	}
}

// deliverTo puts a message straight into a role's inbox, as the daemon would.
func deliverTo(t *testing.T, s *Store, from, to string, priority int, note string) Entry {
	t.Helper()

	h := validNote()
	h.From, h.To, h.Priority, h.Note = from, []string{to}, priority, note

	entry, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deliver(entry.Handoff, to); err != nil {
		t.Fatal(err)
	}
	// The sender side is not under test here.
	if _, err := s.MoveTo(entry.Path, from, BoxSent); err != nil {
		t.Fatal(err)
	}

	return entry
}

// tick advances the store clock so file names sort by arrival.
func tick(s *Store, base time.Time, n int) {
	s.Now = func() time.Time { return base.Add(time.Duration(n) * time.Second) }
}

func TestReadyReturnsNoTaskWhenIdle(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	got, err := l.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Empty() {
		t.Errorf("selected %+v from an empty inbox", got.Entries)
	}
}

func TestReadyTaskModeMovesOneItemToCurrent(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"coder": ModeTask}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "specifier", "coder", 10, "low")
	tick(s, base, 2)
	deliverTo(t, s, "architect", "coder", 30, "high")

	got, err := l.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Entries) != 1 || got.Entries[0].Note != "high" {
		t.Fatalf("selected %+v, want the priority-30 item", got.Entries)
	}

	current, err := s.Current("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].Note != "high" {
		t.Errorf("current = %+v", current)
	}

	// The unselected item stays in the inbox.
	inbox, err := s.Inbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Note != "low" {
		t.Errorf("inbox = %+v, want the low-priority item to remain", inbox)
	}
}

// The same task must never be handed out twice.
func TestReadyReturnsExistingCurrentWork(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"coder": ModeTask}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "specifier", "coder", 10, "first")
	tick(s, base, 2)
	deliverTo(t, s, "architect", "coder", 10, "second")

	first, err := l.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}

	second, err := l.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}

	if len(second.Entries) != 1 || second.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("second ready returned %+v, want the same item", second.Entries)
	}

	current, err := s.Current("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 {
		t.Errorf("current holds %d items, want exactly 1", len(current))
	}
}

// Recovery: state lives on disk, so a fresh Lifecycle sees the same current work.
func TestReadySurvivesRestart(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"coder": ModeTask}))

	deliverTo(t, s, "specifier", "coder", 20, "work")

	first, err := l.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash and restart: brand new store and lifecycle over the
	// same directory.
	restarted := NewStore(rootOf(s), testRoles())
	l2 := NewLifecycle(restarted, modes(map[string]ReceiveMode{"coder": ModeTask}))

	after, err := l2.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != 1 || after.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("after restart got %+v, want the same current item", after.Entries)
	}
}

// rootOf recovers the repository root a store was built from.
func rootOf(s *Store) string {
	// Store.Root is <repo>/.swarm/handoffs
	return s.Root[:len(s.Root)-len("/.swarm/handoffs")]
}

func TestReadyBatchModeTakesEveryTopPriorityItem(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "coder", "refactorer", 20, "a")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "refactorer", 20, "b")
	tick(s, base, 3)
	deliverTo(t, s, "architect", "refactorer", 10, "c")

	got, err := l.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}

	if got.Mode != ModeBatch || len(got.Entries) != 2 {
		t.Fatalf("batch = %+v, want two priority-20 items", got.Entries)
	}
	if got.Entries[0].Note != "a" || got.Entries[1].Note != "b" {
		t.Errorf("batch order = %q, %q", got.Entries[0].Note, got.Entries[1].Note)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Note != "c" {
		t.Errorf("inbox = %+v, want only the priority-10 item", inbox)
	}

	id, err := s.BatchID("refactorer")
	if err != nil || id == "" {
		t.Errorf("BatchID = (%q, %v), want a recorded batch", id, err)
	}
}

// A new lower-priority arrival must not join or displace an active batch.
func TestReadyBatchDoesNotSelectWhileActive(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "coder", "refactorer", 20, "a")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "refactorer", 20, "b")

	first, err := l.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}

	// Something more urgent turns up mid-batch.
	tick(s, base, 3)
	deliverTo(t, s, "architect", "refactorer", 90, "urgent")

	again, err := l.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != len(first.Entries) {
		t.Fatalf("batch changed size to %d", len(again.Entries))
	}
	for i := range again.Entries {
		if again.Entries[i].ID != first.Entries[i].ID {
			t.Fatalf("active batch was replaced")
		}
	}
}

func TestBatchSurvivesRestart(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "coder", "refactorer", 20, "a")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "refactorer", 20, "b")

	first, err := l.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	batchID, err := s.BatchID("refactorer")
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(rootOf(s), testRoles())
	l2 := NewLifecycle(restarted, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	after, err := l2.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != len(first.Entries) {
		t.Fatalf("after restart batch has %d items, want %d", len(after.Entries), len(first.Entries))
	}
	if id, _ := restarted.BatchID("refactorer"); id != batchID {
		t.Errorf("batch id changed across restart: %q -> %q", batchID, id)
	}
}

func TestDoneMovesCurrentToCompleted(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"coder": ModeTask}))

	deliverTo(t, s, "specifier", "coder", 20, "work")

	if _, err := l.Ready("coder"); err != nil {
		t.Fatal(err)
	}

	finished, next, err := l.Done("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 || finished[0].Note != "work" {
		t.Fatalf("finished = %+v", finished)
	}
	if !next.Empty() {
		t.Errorf("next = %+v, want NO_TASK", next.Entries)
	}

	current, err := s.Current("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Errorf("current still holds %d items", len(current))
	}

	completed, err := s.List("coder", BoxCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 1 {
		t.Errorf("completed holds %d items, want 1", len(completed))
	}
}

func TestDoneSelectsNextAutomatically(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"coder": ModeTask}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "specifier", "coder", 30, "first")
	tick(s, base, 2)
	deliverTo(t, s, "architect", "coder", 20, "second")

	if _, err := l.Ready("coder"); err != nil {
		t.Fatal(err)
	}

	finished, next, err := l.Done("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 || finished[0].Note != "first" {
		t.Fatalf("finished = %+v", finished)
	}
	if len(next.Entries) != 1 || next.Entries[0].Note != "second" {
		t.Fatalf("next = %+v, want the priority-20 item", next.Entries)
	}

	// The follow-up work is genuinely current now.
	current, err := s.Current("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].Note != "second" {
		t.Errorf("current = %+v", current)
	}
}

func TestDoneWithNoCurrentWork(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	finished, next, err := l.Done("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 0 {
		t.Errorf("finished = %+v, want nothing", finished)
	}
	if !next.Empty() {
		t.Errorf("next = %+v, want NO_TASK", next.Entries)
	}
}

func TestDoneCompletesWholeBatchAndClearsMarker(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "coder", "refactorer", 20, "a")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "refactorer", 20, "b")
	tick(s, base, 3)
	deliverTo(t, s, "architect", "refactorer", 10, "c")

	if _, err := l.Ready("refactorer"); err != nil {
		t.Fatal(err)
	}

	finished, next, err := l.Done("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 2 {
		t.Fatalf("finished %d items, want the whole batch", len(finished))
	}

	completed, err := s.List("refactorer", BoxCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 {
		t.Errorf("completed holds %d items", len(completed))
	}

	// The leftover lower-priority item becomes the next batch.
	if len(next.Entries) != 1 || next.Entries[0].Note != "c" {
		t.Fatalf("next batch = %+v", next.Entries)
	}

	// And the batch marker tracks the new batch, not the old one.
	if id, err := s.BatchID("refactorer"); err != nil || id == "" {
		t.Errorf("BatchID after done = (%q, %v)", id, err)
	}
}

func TestReadyRejectsUnknownRole(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, func(role string) (ReceiveMode, error) {
		if role == "coder" {
			return ModeTask, nil
		}
		return "", errUnknownRole
	})

	if _, err := l.Ready("intruder"); err == nil {
		t.Error("Ready accepted an unknown role")
	}
}

var errUnknownRole = errRole("unknown role")

type errRole string

func (e errRole) Error() string { return string(e) }
