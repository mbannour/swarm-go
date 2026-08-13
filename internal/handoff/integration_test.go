package handoff

import (
	"io"
	"testing"
)

// fakeAgent drives one role exactly as the runtime prompt instructs a real
// agent to: ready → work → next → done. No AI backend is involved.
type fakeAgent struct {
	t    *testing.T
	role string
	life *Lifecycle
}

// ready returns the agent's current work, or nil for NO_TASK.
func (a *fakeAgent) ready() []Entry {
	a.t.Helper()

	got, err := a.life.Ready(a.role)
	if err != nil {
		a.t.Fatalf("%s ready: %v", a.role, err)
	}
	if got.Empty() {
		return nil
	}

	return got.Entries
}

// advance sends the routed downstream handoff, as `handoff next` does.
func (a *fakeAgent) advance(note string) (Entry, bool) {
	a.t.Helper()

	to, err := NextRole(a.role)
	if err != nil {
		a.t.Fatalf("%s route: %v", a.role, err)
	}

	entry, already, err := a.life.Advance(a.role, Handoff{
		Type: TypeNote, To: []string{to}, Priority: 20, Note: note,
	})
	if err != nil {
		a.t.Fatalf("%s advance: %v", a.role, err)
	}

	return entry, already
}

// done completes current work, as `handoff done` does.
func (a *fakeAgent) done() {
	a.t.Helper()

	if _, _, err := a.life.Done(a.role); err != nil {
		a.t.Fatalf("%s done: %v", a.role, err)
	}
}

func newFakeSwarm(t *testing.T) (*Store, *Lifecycle, *Daemon) {
	t.Helper()

	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	d := NewDaemon(s, []string{"specifier", "coder", "refactorer", "architect"}, nil, nil)
	d.Log = io.Discard

	return s, l, d
}

// The milestone's headline flow: specifier -> coder -> refactorer, with no
// handoff file created by hand anywhere along the way.
func TestFourPackFlowSpecifierToRefactorer(t *testing.T) {
	s, l, d := newFakeSwarm(t)

	specifier := &fakeAgent{t: t, role: "specifier", life: l}
	coder := &fakeAgent{t: t, role: "coder", life: l}
	refactorer := &fakeAgent{t: t, role: "refactorer", life: l}

	// An idle agent must not invent work.
	if work := coder.ready(); work != nil {
		t.Fatalf("coder found work before any was sent: %+v", work)
	}

	// A requirement arrives for the specifier from outside the swarm.
	deliverTo(t, s, "architect", "specifier", 20, "Add rate limiting to login")

	// 1. Specifier picks it up, does its job, routes onward, then finishes.
	if work := specifier.ready(); len(work) != 1 {
		t.Fatalf("specifier ready = %+v", work)
	}
	if _, already := specifier.advance("Specification ready"); already {
		t.Fatal("first specifier handoff reported as already sent")
	}
	specifier.done()

	// Nothing reaches the coder until the daemon runs.
	if len(coder.readyPeekInbox()) != 0 {
		t.Fatal("a handoff appeared in the coder inbox before delivery")
	}

	if got := d.Scan(); got.Delivered != 1 || got.Sent != 1 {
		t.Fatalf("scan after specifier = %+v", got)
	}

	// 2. Coder.
	work := coder.ready()
	if len(work) != 1 || work[0].From != "specifier" {
		t.Fatalf("coder ready = %+v", work)
	}
	if _, already := coder.advance("Implementation complete; tests pass"); already {
		t.Fatal("first coder handoff reported as already sent")
	}
	coder.done()

	if got := d.Scan(); got.Delivered != 1 {
		t.Fatalf("scan after coder = %+v", got)
	}

	// 3. Refactorer receives the coder's work.
	work = refactorer.ready()
	if len(work) != 1 || work[0].From != "coder" {
		t.Fatalf("refactorer ready = %+v", work)
	}
	if work[0].Note != "Implementation complete; tests pass" {
		t.Errorf("refactorer got note %q", work[0].Note)
	}

	// Durable state at the end: both upstream roles finished and recorded.
	for _, role := range []string{"specifier", "coder"} {
		completed, err := s.List(role, BoxCompleted)
		if err != nil {
			t.Fatal(err)
		}
		if len(completed) != 1 {
			t.Errorf("%s completed = %d items, want 1", role, len(completed))
		}

		sent, err := s.List(role, BoxSent)
		if err != nil {
			t.Fatal(err)
		}
		if len(sent) != 1 {
			t.Errorf("%s sent = %d items, want 1", role, len(sent))
		}

		current, err := s.Current(role)
		if err != nil {
			t.Fatal(err)
		}
		if len(current) != 0 {
			t.Errorf("%s still has current work", role)
		}
	}

	// And nothing was rejected or failed along the way.
	rejected, bad, err := s.list(s.RejectedDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 || len(bad) != 0 {
		t.Errorf("messages were rejected: %+v %v", rejected, bad)
	}
}

// readyPeekInbox inspects the inbox without accepting anything.
func (a *fakeAgent) readyPeekInbox() []Entry {
	a.t.Helper()

	entries, err := a.life.Store.Inbox(a.role)
	if err != nil {
		a.t.Fatal(err)
	}

	return entries
}

// The critical correctness case: crash between the downstream send and done.
func TestCrashBetweenSendAndDoneDoesNotDuplicate(t *testing.T) {
	s, l, d := newFakeSwarm(t)

	deliverTo(t, s, "architect", "specifier", 20, "Add rate limiting")

	specifier := &fakeAgent{t: t, role: "specifier", life: l}

	if work := specifier.ready(); len(work) != 1 {
		t.Fatalf("specifier ready = %+v", work)
	}
	first, _ := specifier.advance("Specification ready")

	// ---- crash here: `done` never ran ----
	restartedStore := NewStore(rootOf(s), testRoles())
	restartedLife := NewLifecycle(restartedStore, modes(nil))
	restarted := &fakeAgent{t: t, role: "specifier", life: restartedLife}

	// The agent restarts and asks for work: it gets the same task back.
	resumed := restarted.ready()
	if len(resumed) != 1 || resumed[0].ID != first.SourceID {
		t.Fatalf("after restart ready = %+v, want the original task", resumed)
	}

	// It checks whether it already handed off — and it had.
	status, err := restartedLife.Status("specifier")
	if err != nil {
		t.Fatal(err)
	}
	if !status.DownstreamSent {
		t.Fatal("status does not report the downstream handoff created before the crash")
	}

	// Even if it re-runs the send, no duplicate is produced.
	second, already := restarted.advance("Specification ready")
	if !already || second.ID != first.ID {
		t.Fatalf("re-send produced %s (already=%v), want the original %s", second.ID, already, first.ID)
	}

	restarted.done()

	// Exactly one message reaches the coder.
	if got := d.Scan(); got.Delivered != 1 {
		t.Fatalf("scan = %+v, want exactly one delivery", got)
	}

	inbox, err := restartedStore.Inbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("coder inbox holds %d messages, want exactly 1", len(inbox))
	}
	if inbox[0].ID != first.ID {
		t.Errorf("coder received %s, want %s", inbox[0].ID, first.ID)
	}
}

// Send-before-done: if the downstream send fails, the work must stay current.
func TestFailedSendLeavesWorkActive(t *testing.T) {
	s, l, _ := newFakeSwarm(t)

	deliverTo(t, s, "architect", "specifier", 20, "Add rate limiting")

	if _, err := l.Ready("specifier"); err != nil {
		t.Fatal(err)
	}

	// An invalid downstream message: no note.
	_, _, err := l.Advance("specifier", Handoff{Type: TypeNote, To: []string{"coder"}, Priority: 20})
	if err == nil {
		t.Fatal("an invalid downstream handoff was accepted")
	}

	// The agent must still hold its work, so it can retry.
	current, err := s.Current("specifier")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 {
		t.Fatalf("current work was lost after a failed send: %+v", current)
	}

	// Retrying properly succeeds and is recorded.
	if _, already, err := l.Advance("specifier", Handoff{
		Type: TypeNote, To: []string{"coder"}, Priority: 20, Note: "Specification ready",
	}); err != nil || already {
		t.Fatalf("retry = (%v, %v)", already, err)
	}
}

// A woken agent that finds nothing must stay idle rather than inventing work.
func TestIdleAgentStaysIdle(t *testing.T) {
	_, l, d := newFakeSwarm(t)

	coder := &fakeAgent{t: t, role: "coder", life: l}

	for i := 0; i < 3; i++ {
		if work := coder.ready(); work != nil {
			t.Fatalf("ready invented work: %+v", work)
		}
		d.Scan()
	}

	status, err := l.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if status.State() != "waiting" {
		t.Errorf("idle agent state = %q, want waiting", status.State())
	}
}
