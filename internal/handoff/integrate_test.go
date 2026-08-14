package handoff

import (
	"fmt"
	"testing"
)

// fakeIntegrator records what it was asked to integrate.
type fakeIntegrator struct {
	calls   []string
	method  string
	local   string
	already bool
	err     error
}

func (f *fakeIntegrator) Integrate(worktree, branch, commit string) (string, string, bool, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s|%s|%s", worktree, branch, commit))
	if f.err != nil {
		return "", "", false, f.err
	}
	return f.method, f.local, f.already, nil
}

// gitWorkFor gives a role current work carrying a canonical commit.
func gitWorkFor(t *testing.T, s *Store, l *Lifecycle, role string) Entry {
	t.Helper()

	h := validGit()
	h.From, h.To = "coder", []string{role}

	entry, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}

	// The daemon stamps the canonical commit at delivery time.
	delivered := entry.Handoff
	delivered.CanonicalCommit = canonicalOfValidCommit
	if _, _, err := s.Deliver(delivered, role); err != nil {
		t.Fatal(err)
	}

	selection, err := l.Ready(role)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Empty() {
		t.Fatal("no work was accepted")
	}

	return selection.Entries[0]
}

func TestIntegrateRecordsMetadata(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	gitWorkFor(t, s, l, "refactorer")

	integrator := &fakeIntegrator{method: "cherry-pick", local: "abc123localsha"}

	result, err := l.Integrate("refactorer", "/repo/.swarm/worktrees/wt-refactorer", "swarm/refactorer", integrator)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Required {
		t.Error("a git_handoff was reported as needing no integration")
	}
	if result.Method != "cherry-pick" || result.LocalCommit != "abc123localsha" {
		t.Errorf("result = %+v", result)
	}
	if result.SourceCommit != canonicalOfValidCommit {
		t.Errorf("source commit = %q", result.SourceCommit)
	}

	// The integrator was asked about the configured worktree and branch.
	if len(integrator.calls) != 1 {
		t.Fatalf("calls = %v", integrator.calls)
	}
	want := "/repo/.swarm/worktrees/wt-refactorer|swarm/refactorer|" + canonicalOfValidCommit
	if integrator.calls[0] != want {
		t.Errorf("call = %q, want %q", integrator.calls[0], want)
	}

	// The metadata is durable: a fresh read sees it.
	current, err := s.Current("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	stored := current[0]
	if stored.IntegrationStatus != IntegrationDone {
		t.Errorf("integration_status = %q", stored.IntegrationStatus)
	}
	if stored.IntegrationMethod != "cherry-pick" {
		t.Errorf("integration_method = %q", stored.IntegrationMethod)
	}
	if stored.LocalCommit != "abc123localsha" {
		t.Errorf("local_commit = %q", stored.LocalCommit)
	}
	if stored.CanonicalCommit != canonicalOfValidCommit {
		t.Errorf("the source commit was overwritten: %q", stored.CanonicalCommit)
	}
	if stored.IntegratedAt.IsZero() {
		t.Error("integrated_at was not recorded")
	}
}

// Source and local identity must both survive a round trip through the file.
func TestIntegrationMetadataRoundTrips(t *testing.T) {
	h := validGit()
	h.CanonicalCommit = canonicalOfValidCommit
	h.IntegrationStatus = IntegrationDone
	h.IntegrationMethod = "cherry-pick"
	h.LocalCommit = "91b7f11aaa"

	got, err := Unmarshal([]byte(Marshal(h)))
	if err != nil {
		t.Fatal(err)
	}

	if got.CanonicalCommit != h.CanonicalCommit || got.LocalCommit != h.LocalCommit {
		t.Errorf("commit mapping lost: source=%q local=%q", got.CanonicalCommit, got.LocalCommit)
	}
	if got.IntegrationMethod != "cherry-pick" || got.IntegrationStatus != IntegrationDone {
		t.Errorf("integration state lost: %+v", got)
	}
}

func TestIntegrateNoteRequiresNothing(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	deliverTo(t, s, "specifier", "coder", 20, "just a note")
	if _, err := l.Ready("coder"); err != nil {
		t.Fatal(err)
	}

	integrator := &fakeIntegrator{}

	result, err := l.Integrate("coder", "/repo/.swarm/worktrees/wt-coder", "swarm/coder", integrator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Required {
		t.Error("a note was reported as needing integration")
	}
	if len(integrator.calls) != 0 {
		t.Errorf("Git was consulted for a note: %v", integrator.calls)
	}
}

func TestIntegrateFailureIsRecordedAndWorkStaysCurrent(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	gitWorkFor(t, s, l, "refactorer")

	integrator := &fakeIntegrator{err: fmt.Errorf("cherry-pick conflict applying 71ae82cc13")}

	if _, err := l.Integrate("refactorer", "/wt", "swarm/refactorer", integrator); err == nil {
		t.Fatal("a failed integration reported success")
	}

	current, err := s.Current("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 {
		t.Fatalf("work is no longer current: %+v", current)
	}
	if current[0].IntegrationStatus != IntegrationFailed {
		t.Errorf("integration_status = %q, want failed", current[0].IntegrationStatus)
	}
	if current[0].IntegrationError == "" {
		t.Error("no reason was recorded")
	}
	// The work is still there to retry, and NeedsIntegration still says so.
	if !current[0].NeedsIntegration() {
		t.Error("a failed integration is not reported as still needed")
	}
}

func TestIntegrateIsIdempotentAtLifecycleLevel(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	gitWorkFor(t, s, l, "refactorer")

	first := &fakeIntegrator{method: "fast-forward", local: canonicalOfValidCommit}
	if _, err := l.Integrate("refactorer", "/wt", "swarm/refactorer", first); err != nil {
		t.Fatal(err)
	}

	// The integrator now reports the commit as already present, as Git would.
	second := &fakeIntegrator{method: "none", local: canonicalOfValidCommit, already: true}
	result, err := l.Integrate("refactorer", "/wt", "swarm/refactorer", second)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Already {
		t.Error("the second integration did not report the commit as already present")
	}

	current, _ := s.Current("refactorer")
	if current[0].IntegrationStatus != IntegrationDone {
		t.Errorf("status after re-integration = %q", current[0].IntegrationStatus)
	}
}

// Integration state must survive a restart, so a resumed agent does not reapply.
func TestIntegrationStateSurvivesRestart(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	gitWorkFor(t, s, l, "refactorer")

	if _, err := l.Integrate("refactorer", "/wt", "swarm/refactorer",
		&fakeIntegrator{method: "fast-forward", local: canonicalOfValidCommit}); err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(rootOf(s), testRoles())
	life := NewLifecycle(restarted, modes(nil))

	selection, err := life.Ready("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if selection.Empty() {
		t.Fatal("current work was lost")
	}
	if selection.Entries[0].NeedsIntegration() {
		t.Error("after a restart the work is reported as needing integration again")
	}
	if selection.Entries[0].IntegrationMethod != "fast-forward" {
		t.Errorf("integration method was lost: %q", selection.Entries[0].IntegrationMethod)
	}
}

func TestIntegrateWithoutCurrentWorkFails(t *testing.T) {
	s := newTestStore(t)
	l := NewLifecycle(s, modes(nil))

	if _, err := l.Integrate("coder", "/wt", "swarm/coder", &fakeIntegrator{}); err == nil {
		t.Fatal("integration succeeded with no current work")
	}
}

// A sender must not be able to claim its own handoff is already integrated.
func TestSendClearsIntegrationMetadata(t *testing.T) {
	s := newTestStore(t)

	h := validGit()
	h.IntegrationStatus = IntegrationDone
	h.IntegrationMethod = "fast-forward"
	h.LocalCommit = "fabricated"

	entry, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}

	if entry.IntegrationStatus != "" || entry.LocalCommit != "" || entry.IntegrationMethod != "" {
		t.Errorf("sender-supplied integration metadata was kept: %+v", entry.Handoff)
	}
}
