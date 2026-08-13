package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/mbannour/swarm-go/internal/handoff"
)

const demoTask = "DEMO-1"

const demoRequirement = "Implement a discount calculator: calculate(price, discountPercent), " +
	"100 with 20% returns 80, 50 with 0% returns 50, discounts outside 0..100 are invalid, with tests"

// TestFourPackEndToEnd drives one requirement through the whole cycle:
//
//	developer → specifier → coder → refactorer → architect → specifier
//
// and asserts the durable state at every hop.
func TestFourPackEndToEnd(t *testing.T) {
	s := newSwarm(t)

	// ---- the developer submits a requirement -----------------------------
	submitted := s.submit(demoTask, demoRequirement)

	if got := s.inbox("specifier"); len(got) != 1 || got[0].ID != submitted.ID {
		t.Fatalf("specifier inbox = %+v, want the submitted requirement", got)
	}
	if s.inbox("specifier")[0].From != handoff.SystemSender {
		t.Errorf("requirement sender = %q, want the system boundary", s.inbox("specifier")[0].From)
	}

	// ---- specifier -------------------------------------------------------
	h1 := s.agent("specifier").cycle(demoTask)

	if got := s.outbox("specifier"); len(got) != 1 {
		t.Fatalf("specifier outbox = %d messages, want 1", len(got))
	}
	if h1.SourceID != submitted.ID {
		t.Errorf("h1 source = %q, want the requirement %q", h1.SourceID, submitted.ID)
	}

	if got := s.deliver(); got.Delivered != 1 || got.Sent != 1 {
		t.Fatalf("delivery after specifier = %+v", got)
	}
	if got := s.inbox("coder"); len(got) != 1 || got[0].From != "specifier" {
		t.Fatalf("coder inbox = %+v", got)
	}

	// The specifier's handoff carries a resolvable commit.
	if got := s.inbox("coder")[0]; got.CanonicalCommit == "" {
		t.Error("specifier handoff carries no canonical commit")
	} else if _, err := gitQuiet(s.root, "cat-file", "-e", got.CanonicalCommit); err != nil {
		t.Errorf("canonical commit %s does not exist: %v", got.CanonicalCommit, err)
	}

	// ---- coder -----------------------------------------------------------
	h2 := s.agent("coder").cycle(demoTask)

	if h2.Type != handoff.TypeGit {
		t.Errorf("coder handoff type = %q, want git_handoff", h2.Type)
	}
	if h2.Commit == "" || len(h2.Commit) != 10 {
		t.Errorf("coder commit = %q, want a 10-character abbreviation", h2.Commit)
	}

	if got := s.deliver(); got.Delivered != 1 {
		t.Fatalf("delivery after coder = %+v", got)
	}

	coderWork := s.inbox("refactorer")
	if len(coderWork) != 1 || coderWork[0].Task != demoTask {
		t.Fatalf("refactorer inbox = %+v", coderWork)
	}
	if coderWork[0].CanonicalCommit == "" {
		t.Fatal("the coder's commit was not canonicalised")
	}
	// The delivered commit really contains the implementation.
	files, err := gitQuiet(s.root, "show", "--name-only", "--format=", coderWork[0].CanonicalCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "calculator.go") || !strings.Contains(files, "calculator_test.go") {
		t.Errorf("the coder's commit does not contain the implementation:\n%s", files)
	}

	// ---- refactorer ------------------------------------------------------
	s.agent("refactorer").cycle(demoTask)

	if got := s.deliver(); got.Delivered != 1 {
		t.Fatalf("delivery after refactorer = %+v", got)
	}
	if got := s.inbox("architect"); len(got) != 1 || got[0].From != "refactorer" {
		t.Fatalf("architect inbox = %+v", got)
	}

	// ---- architect -------------------------------------------------------
	s.agent("architect").cycle(demoTask)

	if got := s.deliver(); got.Delivered != 1 {
		t.Fatalf("delivery after architect = %+v", got)
	}

	// ---- back to the specifier: the cycle closes -------------------------
	closing := s.inbox("specifier")
	if len(closing) != 1 || closing[0].From != "architect" {
		t.Fatalf("specifier inbox at cycle close = %+v", closing)
	}

	final := s.agent("specifier")
	if work := final.ready(); work == nil {
		t.Fatal("specifier did not receive the architect's result")
	}
	final.done()

	// ---- final assertions ------------------------------------------------
	assertNoStuckWork(t, s)
	assertQueuesClean(t, s)
	assertCompletedCounts(t, s)
	assertWorktreeIsolation(t, s)
	assertImplementationLanded(t, s)
	assertTraceable(t, s)
}

// assertNoStuckWork checks that nothing was left half-processed.
func assertNoStuckWork(t *testing.T, s *swarm) {
	t.Helper()

	for _, role := range roleNames {
		if got := s.current(role); len(got) != 0 {
			t.Errorf("%s still holds current work: %+v", role, got)
		}
		if got := s.inbox(role); len(got) != 0 {
			t.Errorf("%s inbox is not empty: %+v", role, got)
		}
		if got := s.outbox(role); len(got) != 0 {
			t.Errorf("%s outbox is not empty: %+v", role, got)
		}
	}
}

// assertQueuesClean checks that nothing failed or was rejected.
func assertQueuesClean(t *testing.T, s *swarm) {
	t.Helper()

	for _, role := range roleNames {
		if got := s.failed(role); len(got) != 0 {
			t.Errorf("%s has failed handoffs: %+v", role, got)
		}
	}
	if got := s.rejected(); len(got) != 0 {
		t.Errorf("handoffs were rejected: %+v", got)
	}
}

// assertCompletedCounts checks every role finished exactly what it was given.
func assertCompletedCounts(t *testing.T, s *swarm) {
	t.Helper()

	want := map[string]int{
		"specifier":  2, // the requirement, and the architect's closing result
		"coder":      1,
		"refactorer": 1,
		"architect":  1,
	}

	for role, n := range want {
		if got := s.completed(role); len(got) != n {
			t.Errorf("%s completed %d items, want %d", role, len(got), n)
		}
	}
}

// assertWorktreeIsolation proves the roles really are working in separate
// checkouts, using Git rather than assumption.
func assertWorktreeIsolation(t *testing.T, s *swarm) {
	t.Helper()

	seen := map[string]string{}
	for _, role := range roleNames {
		tree := s.trees[role]

		if other, clash := seen[tree]; clash {
			t.Fatalf("%s and %s share the worktree %s", role, other, tree)
		}
		seen[tree] = role

		// Each worktree is on its own branch.
		branch := strings.TrimSpace(runGit(t, tree, "rev-parse", "--abbrev-ref", "HEAD"))
		if branch != "swarm/"+role {
			t.Errorf("%s is on branch %q, want swarm/%s", role, branch, role)
		}

		// And none of them is carrying another role's uncommitted changes.
		if status := strings.TrimSpace(runGit(t, tree, "status", "--porcelain")); status != "" {
			t.Errorf("%s has uncommitted changes:\n%s", role, status)
		}
	}

	// A file committed only on the coder's branch must not be present in the
	// refactorer's checkout until it is merged there.
	specOnly := strings.TrimSpace(runGit(t, s.trees["specifier"], "log", "--oneline", "-1"))
	coderOnly := strings.TrimSpace(runGit(t, s.trees["coder"], "log", "--oneline", "-1"))
	if specOnly == coderOnly {
		t.Errorf("specifier and coder branches point at the same commit: %q", specOnly)
	}
}

// assertImplementationLanded checks the demo project really was built.
func assertImplementationLanded(t *testing.T, s *swarm) {
	t.Helper()

	// The refactorer's branch holds the final implementation.
	out := runGit(t, s.trees["refactorer"], "show", "HEAD:demo/calculator/calculator.go")
	if !strings.Contains(out, "func Calculate(") {
		t.Errorf("the final implementation is missing Calculate:\n%s", out)
	}
	if !strings.Contains(out, "validDiscount") {
		t.Errorf("the refactorer's change did not land:\n%s", out)
	}

	spec := runGit(t, s.trees["specifier"], "show", "HEAD:demo/calculator/SPEC.md")
	if !strings.Contains(spec, "returns 80") {
		t.Errorf("the specification is missing its acceptance criteria:\n%s", spec)
	}

	review := runGit(t, s.trees["architect"], "show", "HEAD:demo/calculator/REVIEW.md")
	if !strings.Contains(review, "Verdict") {
		t.Errorf("the architecture review is missing its verdict:\n%s", review)
	}
}

// assertTraceable reconstructs the cycle from durable metadata alone.
func assertTraceable(t *testing.T, s *swarm) {
	t.Helper()

	events, err := s.store.Trace(demoTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("trace has %d events, want the whole cycle: %+v", len(events), events)
	}

	// Every hop of the route appears, in order.
	var hops []string
	for _, e := range events {
		hops = append(hops, e.From+"->"+e.Owner)
	}
	joined := strings.Join(hops, " ")

	for _, want := range []string{
		"specifier->coder",
		"coder->refactorer",
		"refactorer->architect",
		"architect->specifier",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace is missing the hop %s:\n%s", want, joined)
		}
	}

	// The chain links back through source ids, so the cycle is one story.
	chain := handoff.TraceChain(events)
	if len(chain) == 0 {
		t.Error("no handoff records its source")
	}

	// Every git_handoff in the trace names a commit that exists.
	for _, e := range events {
		if e.Type == handoff.TypeGit && e.CanonicalCommit != "" {
			if _, err := gitQuiet(s.root, "cat-file", "-e", e.CanonicalCommit); err != nil {
				t.Errorf("trace references a missing commit %s", e.CanonicalCommit)
			}
		}
	}
}

// TestRestartDuringActiveWork stops and restarts while the coder is mid-task.
func TestRestartDuringActiveWork(t *testing.T) {
	s := newSwarm(t)

	s.submit(demoTask, demoRequirement)
	s.agent("specifier").cycle(demoTask)
	s.deliver()

	// The coder accepts the work but does not finish it.
	coder := s.agent("coder")
	accepted := coder.ready()
	if accepted == nil {
		t.Fatal("coder had no work")
	}
	acceptedID := accepted[0].ID

	// ---- swarm stop / swarm start ----------------------------------------
	s.restart()

	// The same task comes back, and nothing new was selected.
	resumed := s.agent("coder").ready()
	if len(resumed) != 1 || resumed[0].ID != acceptedID {
		t.Fatalf("after restart coder got %+v, want the original task %s", resumed, acceptedID)
	}
	if got := s.current("coder"); len(got) != 1 {
		t.Fatalf("coder holds %d current items after restart", len(got))
	}

	// Work continues normally through to the refactorer.
	after := s.agent("coder")
	commit := after.work()
	if _, already := after.advance(demoTask, commit, "resumed after restart"); already {
		t.Fatal("a duplicate handoff was produced after restart")
	}
	after.done()

	if got := s.deliver(); got.Delivered != 1 {
		t.Fatalf("delivery after resumed work = %+v", got)
	}
	if got := s.inbox("refactorer"); len(got) != 1 {
		t.Fatalf("refactorer inbox = %+v, want exactly one message", got)
	}

	// And exactly one message reached the refactorer overall.
	if got := s.completed("coder"); len(got) != 1 {
		t.Errorf("coder completed %d items, want 1", len(got))
	}
}

// TestCrashBetweenSendAndDone is the duplicate-prevention case: the downstream
// handoff exists, but the process died before `done` ran.
func TestCrashBetweenSendAndDone(t *testing.T) {
	s := newSwarm(t)

	s.submit(demoTask, demoRequirement)
	s.agent("specifier").cycle(demoTask)
	s.deliver()

	coder := s.agent("coder")
	if coder.ready() == nil {
		t.Fatal("coder had no work")
	}

	commit := coder.work()
	first, already := coder.advance(demoTask, commit, "implementation complete")
	if already {
		t.Fatal("the first handoff was reported as already sent")
	}

	// ---- crash: `done` never ran -----------------------------------------
	s.restart()

	resumed := s.agent("coder")
	work := resumed.ready()
	if len(work) != 1 {
		t.Fatalf("coder did not resume its work: %+v", work)
	}

	// The orchestrator knows the downstream handoff already exists.
	status, err := s.life.Status("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !status.DownstreamSent {
		t.Fatal("status does not report the handoff created before the crash")
	}

	// Re-running the send returns the original rather than duplicating.
	second, already := resumed.advance(demoTask, commit, "implementation complete")
	if !already || second.ID != first.ID {
		t.Fatalf("re-send produced %s (already=%v), want the original %s", second.ID, already, first.ID)
	}

	resumed.done()
	s.deliver()

	// Exactly one message reached the refactorer.
	if got := s.inbox("refactorer"); len(got) != 1 {
		t.Fatalf("refactorer inbox holds %d messages, want exactly 1", len(got))
	}
	if got := s.outbox("coder"); len(got) != 0 {
		t.Errorf("coder outbox still holds %+v", got)
	}
}

// TestBatchRoleTakesEveryTopPriorityItem covers the other receive mode inside
// the same acceptance setting.
func TestBatchRoleTakesEveryTopPriorityItem(t *testing.T) {
	s := newSwarm(t)

	// The refactorer receives in batch mode for this test.
	s.life = handoff.NewLifecycle(s.store, func(role string) (handoff.ReceiveMode, error) {
		if role == "refactorer" {
			return handoff.ModeBatch, nil
		}
		return handoff.ModeTask, nil
	})

	for i, spec := range []struct {
		priority int
		note     string
	}{
		{20, "first at 20"},
		{20, "second at 20"},
		{10, "lower priority"},
	} {
		entry, err := s.store.Send(handoff.Handoff{
			Type: handoff.TypeNote, From: "coder", To: []string{"refactorer"},
			Priority: spec.priority, Note: spec.note,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if _, _, err := s.store.Deliver(entry.Handoff, "refactorer"); err != nil {
			t.Fatal(err)
		}
	}

	selection, err := s.life.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Entries) != 2 || selection.Priority != 20 {
		t.Fatalf("batch = %d items at priority %d, want the two at 20",
			len(selection.Entries), selection.Priority)
	}
	if got := s.inbox("refactorer"); len(got) != 1 || got[0].Priority != 10 {
		t.Errorf("inbox after batch = %+v, want only the priority-10 item", got)
	}
}

// TestDaemonDeliversWithinDeadline exercises the running loop rather than a
// single scan, with bounded polling.
func TestDaemonDeliversWithinDeadline(t *testing.T) {
	s := newSwarm(t)

	s.submit(demoTask, demoRequirement)
	s.agent("specifier").cycle(demoTask)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if s.daemon.Scan().Delivered > 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	<-done

	waitFor(t, "the coder's inbox", 2*time.Second, func() bool {
		entries, err := s.store.Inbox("coder")
		return err == nil && len(entries) == 1
	})
}
