package lifecycle

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mbannour/swarm-go/internal/diagnostics"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/repair"
)

// recoveryFixture is a real repository with real worktrees and real handoff
// state, wired to the inspector. Sessions and agents are faked so failures can
// be induced exactly; the Git, handoff, daemon-metadata and lock paths are real.
type recoveryFixture struct {
	t         *testing.T
	root      string
	mgr       *Manager
	inspector *Inspector
	store     *handoff.Store
	sessions  *fakeSessions
	agents    *fakeAgents
	wtMgr     *git.WorktreeManager
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()

	if !git.Available() {
		t.Skip("git not available")
	}

	root := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "swarm@example.com")
	runGit("config", "user.name", "swarm")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "initial")

	wtMgr, err := git.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	names := []string{"specifier", "coder", "refactorer", "architect"}
	store := handoff.NewStore(root, handoff.NewRoles(names))
	if err := store.EnsureDirs(names); err != nil {
		t.Fatal(err)
	}

	sessions, agents := newFakeSessions(), newFakeAgents()

	life := handoff.NewLifecycle(store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})

	mgr := &Manager{
		RepoRoot:   root,
		Roles:      testRoles(root),
		Worktrees:  GitWorktrees{Mgr: wtMgr},
		Sessions:   sessions,
		Agents:     agents,
		Work:       HandoffWork{Store: store, Life: life, Roles: names},
		Env:        newFakeEnv(filepath.Join(root, "bin", "swarm")),
		Out:        io.Discard,
		SkipDaemon: true,
	}

	f := &recoveryFixture{
		t: t, root: root, mgr: mgr, store: store,
		sessions: sessions, agents: agents, wtMgr: wtMgr,
	}

	f.inspector = &Inspector{
		Mgr:      mgr,
		Git:      GitInspection{Mgr: wtMgr},
		Handoffs: HandoffInspection{Store: store},
		Tmux:     &fakeTmux{},
	}

	return f
}

// start brings the fake swarm up.
func (f *recoveryFixture) start() {
	f.t.Helper()

	if _, err := f.mgr.Start(context.Background()); err != nil {
		f.t.Fatalf("start: %v", err)
	}
	// SkipDaemon means the lifecycle does not run one; simulate a live daemon
	// where a test needs the swarm to look fully up.
}

func (f *recoveryFixture) diagnose() diagnostics.Report { return f.inspector.Diagnose() }

func (f *recoveryFixture) repairPlan() repair.Plan {
	f.t.Helper()

	plan, _, err := f.inspector.Repair(true)
	if err != nil {
		f.t.Fatalf("dry-run repair: %v", err)
	}

	return plan
}

func (f *recoveryFixture) repair() repair.Report {
	f.t.Helper()

	_, report, err := f.inspector.Repair(false)
	if err != nil {
		f.t.Fatalf("repair: %v", err)
	}

	return report
}

// fakeTmux stands in for the tmux server so socket states can be simulated.
type fakeTmux struct {
	socket  string
	alive   bool
	removed bool
}

func (f *fakeTmux) SocketPath() string { return f.socket }
func (f *fakeTmux) ServerAlive() bool  { return f.alive }
func (f *fakeTmux) RemoveSocket() error {
	f.removed = true
	if f.socket != "" {
		return os.Remove(f.socket)
	}
	return nil
}

func hasCode(report diagnostics.Report, code string) bool {
	for _, d := range report.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func hasAction(plan repair.Plan, kind repair.Kind, component string) bool {
	for _, a := range plan.Actions {
		if a.Kind == kind && (component == "" || a.Component == component) {
			return true
		}
	}
	return false
}

// ---- A. daemon crash ----------------------------------------------------

func TestRecoveryDaemonCrash(t *testing.T) {
	f := newRecoveryFixture(t)
	f.start()
	f.inspector.Tmux = &fakeTmux{alive: true}

	// A daemon was running and died, leaving its pid file behind.
	if err := EnsureRuntimeDir(f.root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DaemonPIDPath(f.root),
		[]byte(`{"pid":999999,"repository":"`+f.root+`","started_at":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A handoff is waiting to be delivered.
	entry, err := f.store.Send(handoff.Handoff{
		Type: handoff.TypeNote, From: "specifier", To: []string{"coder"},
		Priority: 20, Note: "please implement",
	})
	if err != nil {
		t.Fatal(err)
	}

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeDaemonNotRunning) {
		t.Fatalf("daemon crash not detected: %+v", report.Diagnostics)
	}
	if !hasCode(report, diagnostics.CodeDaemonStalePID) {
		t.Error("stale pid metadata not detected")
	}
	if report.Health != diagnostics.HealthDegraded {
		t.Errorf("health = %q, want degraded", report.Health)
	}

	// The handoff is untouched by diagnosis.
	if _, err := os.Stat(entry.Path); err != nil {
		t.Errorf("the queued handoff was disturbed: %v", err)
	}

	// Repair clears the stale metadata. Starting a real daemon needs a real
	// binary, so that step is expected to fail here — what matters is that the
	// metadata is cleaned and nothing else is harmed.
	f.repair()

	if _, err := os.Stat(DaemonPIDPath(f.root)); !os.IsNotExist(err) {
		t.Error("stale pid file survived repair")
	}
	if _, err := os.Stat(entry.Path); err != nil {
		t.Errorf("repair lost the queued handoff: %v", err)
	}

	// And once a daemon runs again, the waiting handoff is delivered.
	daemon := handoff.NewDaemon(f.store, []string{"specifier", "coder", "refactorer", "architect"},
		nil, git.NewRepo(f.root))
	daemon.Log = io.Discard
	if got := daemon.Scan(); got.Delivered != 1 {
		t.Fatalf("scan after recovery = %+v, want the waiting handoff delivered", got)
	}
}

// ---- B. missing session -------------------------------------------------

func TestRecoveryMissingSession(t *testing.T) {
	f := newRecoveryFixture(t)
	f.start()
	f.inspector.Tmux = &fakeTmux{alive: true}

	// Give the coder work in progress, then kill its session.
	giveCurrentWork(t, f.store, "coder")
	delete(f.sessions.present, "coder")
	delete(f.agents.running, "coder")

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeSessionMissing) {
		t.Fatalf("missing session not detected: %+v", report.Diagnostics)
	}

	plan := f.repairPlan()
	if !hasAction(plan, repair.KindCreateSession, "coder") {
		t.Fatalf("plan does not recreate the coder session: %+v", plan.Actions)
	}

	f.repair()

	if !f.sessions.present["coder"] {
		t.Error("the coder session was not recreated")
	}
	if !f.agents.running["coder"] {
		t.Error("the coder agent was not restarted with its session")
	}

	// Untouched roles were not restarted.
	if len(f.sessions.created) != 5 {
		t.Errorf("sessions created = %v, want only one extra", f.sessions.created)
	}

	// Current work survived the repair.
	current, err := f.store.List("coder", handoff.BoxCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 {
		t.Errorf("current work was lost by repair: %+v", current)
	}

	if after := f.diagnose(); hasCode(after, diagnostics.CodeSessionMissing) {
		t.Error("the session is still reported missing after repair")
	}
}

// ---- C. missing agent ---------------------------------------------------

func TestRecoveryMissingAgent(t *testing.T) {
	f := newRecoveryFixture(t)
	f.start()
	f.inspector.Tmux = &fakeTmux{alive: true}

	// The session survives; the agent exited.
	delete(f.agents.running, "refactorer")

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeAgentMissing) {
		t.Fatalf("missing agent not detected: %+v", report.Diagnostics)
	}

	plan := f.repairPlan()
	if !hasAction(plan, repair.KindStartAgent, "refactorer") {
		t.Fatalf("plan does not restart the agent: %+v", plan.Actions)
	}
	if hasAction(plan, repair.KindCreateSession, "refactorer") {
		t.Error("plan recreates a session that is still alive")
	}

	f.repair()

	if !f.agents.running["refactorer"] {
		t.Error("the agent was not restarted")
	}
	if !f.sessions.present["refactorer"] {
		t.Error("repair disturbed the surviving session")
	}
}

// ---- D. stale socket ----------------------------------------------------

func TestRecoveryStaleSocket(t *testing.T) {
	f := newRecoveryFixture(t)

	// A socket file with no server behind it.
	socket := filepath.Join(t.TempDir(), "swarm-go-test", "abc123.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tmux := &fakeTmux{socket: socket, alive: false}
	f.inspector.Tmux = tmux

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeSocketStale) {
		t.Fatalf("stale socket not detected: %+v", report.Diagnostics)
	}

	f.repair()

	if !tmux.removed {
		t.Error("the stale socket was not removed")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Error("the socket file is still present")
	}
}

// A live server's socket must never be removed.
func TestRepairRefusesToRemoveLiveSocket(t *testing.T) {
	f := newRecoveryFixture(t)
	tmux := &fakeTmux{socket: filepath.Join(t.TempDir(), "live.sock"), alive: true}
	f.inspector.Tmux = tmux

	if err := os.WriteFile(tmux.socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.inspector.RemoveStaleSocket(); err == nil {
		t.Fatal("repair removed a socket with a live server behind it")
	}
	if _, err := os.Stat(tmux.socket); err != nil {
		t.Error("the live socket was deleted")
	}
}

// ---- E. stale PID -------------------------------------------------------

func TestRecoveryStalePIDMetadata(t *testing.T) {
	f := newRecoveryFixture(t)

	if err := EnsureRuntimeDir(f.root); err != nil {
		t.Fatal(err)
	}
	pidFile := DaemonPIDPath(f.root)
	if err := os.WriteFile(pidFile,
		[]byte(`{"pid":999999,"repository":"`+f.root+`","started_at":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeDaemonStalePID) {
		t.Fatalf("stale pid not detected: %+v", report.Diagnostics)
	}

	f.repair()

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("stale pid file was not removed")
	}
}

// ---- F. orphan delivery -------------------------------------------------

func TestRecoveryOrphanDeliveryIsReconciledWithoutDuplication(t *testing.T) {
	f := newRecoveryFixture(t)

	// A handoff was delivered, but the daemon died before retiring the source.
	entry, err := f.store.Send(handoff.Handoff{
		Type: handoff.TypeNote, From: "coder", To: []string{"refactorer"},
		Priority: 20, Note: "ready for review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.Deliver(entry.Handoff, "refactorer"); err != nil {
		t.Fatal(err)
	}

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeOrphanDelivery) {
		t.Fatalf("orphan not detected: %+v", report.Diagnostics)
	}

	before, err := f.store.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("destination inbox = %d, want 1", len(before))
	}

	f.repair()

	// The source is retired to sent/, not redelivered.
	outbox, err := f.store.List("coder", handoff.BoxOutbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 0 {
		t.Errorf("outbox still holds %d message(s)", len(outbox))
	}

	sent, err := f.store.List("coder", handoff.BoxSent)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].ID != entry.ID {
		t.Errorf("sent = %+v, want the reconciled handoff", sent)
	}

	after, err := f.store.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("destination inbox = %d after repair, want exactly 1 (no duplicate)", len(after))
	}
	if after[0].ID != before[0].ID {
		t.Error("the delivered copy was replaced")
	}
}

// ---- dirty worktree safety ---------------------------------------------

// Non-negotiable: repair must never touch uncommitted work.
func TestRepairNeverTouchesDirtyWorktree(t *testing.T) {
	f := newRecoveryFixture(t)
	f.start()
	f.inspector.Tmux = &fakeTmux{alive: true}

	tree := f.mgr.Roles[1].Worktree // coder
	scratch := filepath.Join(tree, "IN-PROGRESS.md")
	contents := "work in progress, do not delete\n"
	if err := os.WriteFile(scratch, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeWorktreeDirty) {
		t.Fatalf("dirty worktree not detected: %+v", report.Diagnostics)
	}

	// It must be reported as needing a human, never as repairable.
	for _, d := range report.Repairable() {
		if d.Code == diagnostics.CodeWorktreeDirty {
			t.Fatal("a dirty worktree was offered as an automatic repair")
		}
	}

	plan := f.repairPlan()
	if hasAction(plan, repair.KindCreateWorktree, "coder") {
		t.Fatal("plan would recreate a worktree that has uncommitted changes")
	}

	f.repair()

	// The file, and its contents, survive untouched.
	got, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("repair deleted uncommitted work: %v", err)
	}
	if string(got) != contents {
		t.Fatalf("repair modified uncommitted work: %q", got)
	}

	// And Git still sees it as dirty: nothing was committed or reset either.
	dirty, err := f.wtMgr.WorktreeDirty(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("repair committed or discarded the user's changes")
	}
}

// ---- missing worktree ---------------------------------------------------

func TestRecoveryMissingWorktree(t *testing.T) {
	f := newRecoveryFixture(t)
	f.start()

	// Remove a worktree the way a stray `rm -rf` would.
	tree := f.mgr.Roles[2].Worktree // refactorer
	if err := os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}

	report := f.diagnose()
	if !hasCode(report, diagnostics.CodeWorktreeMissing) {
		t.Fatalf("missing worktree not detected: %+v", report.Diagnostics)
	}

	// Git still has it registered, so it is ambiguous and must not be guessed.
	for _, d := range report.Diagnostics {
		if d.Code == diagnostics.CodeWorktreeMissing && d.Repairable {
			t.Error("a worktree with live Git registration was marked auto-repairable")
		}
	}

	// After pruning the stale registration, recreating it is unambiguous.
	if err := f.wtMgr.PruneManaged(); err != nil {
		t.Fatal(err)
	}
	if err := f.inspector.CreateWorktree("refactorer"); err != nil {
		t.Fatalf("recreate after prune: %v", err)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Errorf("worktree was not recreated: %v", err)
	}
}

// ---- temp files ---------------------------------------------------------

func TestRepairCleansOnlyManagedTempFiles(t *testing.T) {
	f := newRecoveryFixture(t)

	// An abandoned atomic-write temporary inside the managed tree.
	managed := filepath.Join(f.root, ".swarm", "handoffs", "coder", "outbox", ".tmp-abandoned")
	if err := os.WriteFile(managed, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(managed, old, old); err != nil {
		t.Fatal(err)
	}

	// A file with the same name pattern outside the managed tree.
	outside := filepath.Join(f.root, ".tmp-user-file")
	if err := os.WriteFile(outside, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, old, old); err != nil {
		t.Fatal(err)
	}

	files, err := f.inspector.StaleTempFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if !strings.Contains(path, ".swarm") {
			t.Errorf("a file outside the managed tree was listed: %s", path)
		}
	}

	f.repair()

	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Error("the managed temp file was not cleaned")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("repair removed a file outside the managed tree")
	}
}

// A fresh temporary might be a write in flight, so it is left alone.
func TestRepairLeavesRecentTempFiles(t *testing.T) {
	f := newRecoveryFixture(t)

	fresh := filepath.Join(f.root, ".swarm", "handoffs", "coder", "outbox", ".tmp-inflight")
	if err := os.WriteFile(fresh, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := f.inspector.StaleTempFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if path == fresh {
			t.Error("a temp file written moments ago was treated as abandoned")
		}
	}
}

// ---- concurrency --------------------------------------------------------

// Repair must not race with start, stop or another repair.
func TestRepairHoldsTheLifecycleLock(t *testing.T) {
	f := newRecoveryFixture(t)

	if err := EnsureRuntimeDir(f.root); err != nil {
		t.Fatal(err)
	}

	other, held, err := TryLock(LifecycleLockPath(f.root))
	if err != nil || !held {
		t.Fatalf("could not take the lifecycle lock: (%v, %v)", held, err)
	}

	_, _, err = f.inspector.Repair(false)
	if err == nil {
		other.Unlock()
		t.Fatal("repair ran while another lifecycle operation held the lock")
	}
	if !strings.Contains(err.Error(), "another swarm lifecycle operation") {
		t.Errorf("unhelpful error: %v", err)
	}

	other.Unlock()

	if _, _, err := f.inspector.Repair(true); err != nil {
		t.Errorf("repair after release: %v", err)
	}
}

// Diagnosis must never change anything.
func TestDiagnoseIsReadOnly(t *testing.T) {
	f := newRecoveryFixture(t)

	before := snapshot(t, f.root)
	f.diagnose()
	after := snapshot(t, f.root)

	if before != after {
		t.Errorf("doctor changed the filesystem:\nbefore: %s\nafter:  %s", before, after)
	}
}

// snapshot renders a stable listing of the managed tree.
func snapshot(t *testing.T, root string) string {
	t.Helper()

	var paths []string
	err := filepath.Walk(filepath.Join(root, ".swarm"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return strings.Join(paths, "\n")
}

// giveCurrentWork delivers a message to a role and accepts it.
func giveCurrentWork(t *testing.T, store *handoff.Store, role string) {
	t.Helper()

	entry, err := store.Send(handoff.Handoff{
		Type: handoff.TypeNote, From: "specifier", To: []string{role},
		Priority: 20, Note: "work in progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Deliver(entry.Handoff, role); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveTo(entry.Path, "specifier", handoff.BoxSent); err != nil {
		t.Fatal(err)
	}

	life := handoff.NewLifecycle(store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})
	if _, err := life.Ready(role); err != nil {
		t.Fatal(err)
	}
}
