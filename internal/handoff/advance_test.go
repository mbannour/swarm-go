package handoff

import (
	"testing"
	"time"
)

// startWorkOn delivers a message to a role and accepts it, so the role has
// current work to hand onward.
func startWorkOn(t *testing.T, s *Store, l *Lifecycle, from, role string) Entry {
	t.Helper()

	deliverTo(t, s, from, role, 20, "please do this")

	got, err := l.Ready(role)
	if err != nil {
		t.Fatal(err)
	}
	if got.Empty() {
		t.Fatal("no work was accepted")
	}

	return got.Entries[0]
}

func TestAdvanceCreatesDownstreamHandoff(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	source := startWorkOn(t, s, l, "specifier", "coder")

	entry, already, err := l.Advance("coder", Handoff{
		Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready for refactoring",
	})
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Fatal("a fresh handoff was reported as already sent")
	}
	if entry.SourceID != source.ID {
		t.Errorf("SourceID = %q, want the current work id %q", entry.SourceID, source.ID)
	}
	if entry.From != "coder" {
		t.Errorf("From = %q, want coder", entry.From)
	}

	out, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("outbox holds %d messages, want 1", len(out))
	}
}

func TestAdvanceRequiresCurrentWork(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	_, _, err := l.Advance("coder", Handoff{
		Type: TypeNote, To: []string{"refactorer"}, Priority: 10, Note: "hi",
	})
	if err == nil {
		t.Fatal("Advance succeeded with no current work")
	}
}

// The core crash-safety property: re-running the same advance returns the
// original handoff instead of creating a second one.
func TestAdvanceIsIdempotentForTheSameWork(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	startWorkOn(t, s, l, "specifier", "coder")

	msg := Handoff{Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready"}

	first, already, err := l.Advance("coder", msg)
	if err != nil || already {
		t.Fatalf("first Advance = (%v, %v)", already, err)
	}

	second, already, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}
	if !already {
		t.Fatal("second Advance created a new handoff")
	}
	if second.ID != first.ID {
		t.Errorf("second Advance returned id %q, want %q", second.ID, first.ID)
	}

	out, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("outbox holds %d messages, want exactly 1", len(out))
	}
}

// A restart is exactly this: brand-new Store and Lifecycle over the same tree.
func TestAdvanceIsIdempotentAcrossRestart(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	startWorkOn(t, s, l, "specifier", "coder")

	msg := Handoff{Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready"}

	first, _, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}

	// Crash here: the downstream handoff exists but `done` never ran.
	restarted := NewStore(rootOf(s), testRoles())
	l2 := NewLifecycle(restarted, modes(nil))

	// The agent comes back, sees its current work again...
	resumed, err := l2.Ready("coder")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Empty() {
		t.Fatal("current work was lost across the restart")
	}

	// ...and re-runs the send, which must not duplicate.
	second, already, err := l2.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}
	if !already || second.ID != first.ID {
		t.Fatalf("after restart Advance = (already=%v, id=%s), want the original %s", already, second.ID, first.ID)
	}

	out, err := restarted.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Errorf("outbox holds %d messages after restart, want 1", len(out))
	}
}

// Once delivered, the record lives in sent/ — duplicate detection must still work.
func TestAdvanceDetectsAlreadyDeliveredDownstream(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	startWorkOn(t, s, l, "specifier", "coder")

	msg := Handoff{Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready"}

	first, _, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}

	// The daemon delivers it and retires the source to sent/.
	if _, _, err := s.Deliver(first.Handoff, "refactorer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTo(first.Path, "coder", BoxSent); err != nil {
		t.Fatal(err)
	}

	second, already, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}
	if !already || second.ID != first.ID {
		t.Errorf("Advance after delivery = (already=%v, id=%s), want the original", already, second.ID)
	}
}

// A failed send must be retryable rather than counting as already sent.
func TestAdvanceRetriesAfterFailure(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	startWorkOn(t, s, l, "specifier", "coder")

	msg := Handoff{Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready"}

	first, _, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}

	// The daemon could not complete it.
	if _, err := s.Fail("coder", first.Path, "destination unavailable"); err != nil {
		t.Fatal(err)
	}

	_, already, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("a failed handoff was treated as already sent")
	}
}

// Different work produces different downstream handoffs.
func TestAdvanceAfterDoneSendsAgain(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "specifier", "coder", 20, "first task")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "coder", 20, "second task")

	if _, err := l.Ready("coder"); err != nil {
		t.Fatal(err)
	}

	msg := Handoff{Type: TypeNote, To: []string{"refactorer"}, Priority: 20, Note: "ready"}

	first, _, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}

	// Finish, which pulls in the second task.
	if _, _, err := l.Done("coder"); err != nil {
		t.Fatal(err)
	}

	second, already, err := l.Advance("coder", msg)
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Fatal("new work reused the previous handoff")
	}
	if second.ID == first.ID || second.SourceID == first.SourceID {
		t.Errorf("second handoff shares identity with the first: %+v", second)
	}
}

func TestStatusReportsDownstreamState(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	// Idle.
	status, err := l.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "waiting" || status.DownstreamSent {
		t.Errorf("idle status = %+v", status)
	}

	// Delivered but not accepted.
	deliverTo(t, s, "specifier", "coder", 20, "work")
	status, err = l.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "ready" || status.Inbox != 1 {
		t.Errorf("pending status = %+v", status)
	}

	// Accepted.
	if _, err := l.Ready("coder"); err != nil {
		t.Fatal(err)
	}
	status, err = l.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "working" || len(status.Current) != 1 || status.DownstreamSent {
		t.Errorf("working status = %+v", status)
	}

	// Handed onward.
	if _, _, err := l.Advance("coder", Handoff{
		Type: TypeNote, To: []string{"refactorer"}, Priority: 10, Note: "done",
	}); err != nil {
		t.Fatal(err)
	}
	status, err = l.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !status.DownstreamSent || len(status.Downstream) != 1 {
		t.Errorf("status after send = %+v", status)
	}
}

func TestSourceIDIsStableForABatch(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(map[string]ReceiveMode{"refactorer": ModeBatch}))

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tick(s, base, 1)
	deliverTo(t, s, "coder", "refactorer", 20, "a")
	tick(s, base, 2)
	deliverTo(t, s, "specifier", "refactorer", 20, "b")

	if _, err := l.Ready("refactorer"); err != nil {
		t.Fatal(err)
	}

	first, err := l.SourceID("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.SourceID("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Errorf("batch SourceID is not stable: %q vs %q", first, second)
	}
}
