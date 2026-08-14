package diagnostics

import (
	"encoding/json"
	"fmt"
	"testing"
)

// fakeSystem is a fully controllable system under inspection, so every failure
// mode can be reproduced exactly.
type fakeSystem struct {
	roles []Role

	repoErr     error
	commits     bool
	configErr   error
	promptErr   map[string]error
	runtimeErr  error
	worktrees   map[string]Worktree
	worktreeErr map[string]error
	staleMeta   []string
	tmux        bool
	socket      bool
	serverAlive bool
	sessions    map[string]bool
	agents      map[string]string
	backends    map[string]bool
	daemonState string
	daemonPID   int
	daemonErr   error
	pidFile     bool
	lockHeld    bool
	current     map[string]int
	failed      map[string]int
	rejected    int
	pending     map[string]int
	orphans     []Orphan
	tempFiles   []string
	integration map[string][2]string // role -> {status, reason}
}

func newFakeSystem() *fakeSystem {
	roles := []Role{
		{Name: "specifier", Backend: "codex", ReceiveMode: "task"},
		{Name: "coder", Backend: "codex", ReceiveMode: "task"},
		{Name: "refactorer", Backend: "codex", ReceiveMode: "task"},
		{Name: "architect", Backend: "codex", ReceiveMode: "task"},
	}

	s := &fakeSystem{
		roles: roles, commits: true, tmux: true, socket: true, serverAlive: true,
		daemonState: "running", daemonPID: 4242,
		promptErr: map[string]error{}, worktrees: map[string]Worktree{},
		worktreeErr: map[string]error{}, sessions: map[string]bool{},
		agents: map[string]string{}, backends: map[string]bool{"codex": true},
		current: map[string]int{}, failed: map[string]int{}, pending: map[string]int{},
		integration: map[string][2]string{},
	}

	for _, r := range roles {
		s.worktrees[r.Name] = Worktree{
			State:          WorktreeHealthy,
			Path:           "/repo/.swarm/worktrees/wt-" + r.Name,
			ExpectedBranch: "swarm/" + r.Name,
			ActualBranch:   "swarm/" + r.Name,
			Registered:     true,
		}
		s.sessions[r.Name] = true
		s.agents[r.Name] = "running"
	}

	return s
}

func (s *fakeSystem) RepoRoot() string                 { return "/repo" }
func (s *fakeSystem) Roles() []Role                    { return s.roles }
func (s *fakeSystem) RepoPresent() error               { return s.repoErr }
func (s *fakeSystem) HasCommits() bool                 { return s.commits }
func (s *fakeSystem) ConfigValid() error               { return s.configErr }
func (s *fakeSystem) PromptsPresent(role string) error { return s.promptErr[role] }
func (s *fakeSystem) RuntimeWritable() error           { return s.runtimeErr }

func (s *fakeSystem) InspectWorktree(role string) (Worktree, error) {
	if err := s.worktreeErr[role]; err != nil {
		return Worktree{}, err
	}
	return s.worktrees[role], nil
}

func (s *fakeSystem) StaleWorktreeMetadata() ([]string, error) { return s.staleMeta, nil }
func (s *fakeSystem) TmuxAvailable() bool                      { return s.tmux }
func (s *fakeSystem) SocketPresent() bool                      { return s.socket }
func (s *fakeSystem) ServerAlive() bool                        { return s.serverAlive }

func (s *fakeSystem) SessionAlive(role string) (bool, error) { return s.sessions[role], nil }

func (s *fakeSystem) AgentState(role string) (string, error) {
	state, ok := s.agents[role]
	if !ok {
		return "session-missing", nil
	}
	return state, nil
}

func (s *fakeSystem) BackendAvailable(backend string) bool { return s.backends[backend] }

func (s *fakeSystem) DaemonState() (string, int, error) {
	return s.daemonState, s.daemonPID, s.daemonErr
}

func (s *fakeSystem) DaemonPIDFilePresent() bool       { return s.pidFile }
func (s *fakeSystem) LifecycleLockHeld() (bool, error) { return s.lockHeld, nil }

func (s *fakeSystem) CurrentCount(role string) (int, error)  { return s.current[role], nil }
func (s *fakeSystem) FailedCount(role string) (int, error)   { return s.failed[role], nil }
func (s *fakeSystem) RejectedCount() (int, error)            { return s.rejected, nil }
func (s *fakeSystem) PendingOutbox(role string) (int, error) { return s.pending[role], nil }
func (s *fakeSystem) Orphans() ([]Orphan, error)             { return s.orphans, nil }
func (s *fakeSystem) StaleTempFiles() ([]string, error)      { return s.tempFiles, nil }

func (s *fakeSystem) IntegrationState(role string) (string, string, error) {
	state := s.integration[role]
	return state[0], state[1], nil
}

// find returns the first diagnostic with a code, or false.
func find(report Report, code string) (Diagnostic, bool) {
	for _, d := range report.Diagnostics {
		if d.Code == code {
			return d, true
		}
	}
	return Diagnostic{}, false
}

func TestHealthySystemHasNoDiagnostics(t *testing.T) {
	report := Detect(newFakeSystem())

	if len(report.Diagnostics) != 0 {
		t.Errorf("healthy system produced diagnostics: %+v", report.Diagnostics)
	}
	if report.Health != HealthHealthy {
		t.Errorf("health = %q, want healthy", report.Health)
	}
	if report.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", report.ExitCode())
	}
}

func TestDaemonNotRunningIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.daemonState = "stopped"

	report := Detect(s)

	d, ok := find(report, CodeDaemonNotRunning)
	if !ok {
		t.Fatalf("daemon failure not detected: %+v", report.Diagnostics)
	}
	if !d.Repairable {
		t.Error("a stopped daemon should be repairable")
	}
	if report.Health != HealthDegraded {
		t.Errorf("health = %q, want degraded", report.Health)
	}
	if report.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", report.ExitCode())
	}
}

func TestStalePIDIsDetectedAndRepairable(t *testing.T) {
	s := newFakeSystem()
	s.daemonState = "stopped"
	s.pidFile = true

	report := Detect(s)

	d, ok := find(report, CodeDaemonStalePID)
	if !ok {
		t.Fatal("stale pid metadata not detected")
	}
	if !d.Repairable {
		t.Error("stale pid metadata should be repairable")
	}
}

// A daemon whose identity cannot be verified must never be signalled.
func TestUnverifiedDaemonIsBlocking(t *testing.T) {
	s := newFakeSystem()
	s.daemonState = "failed"

	report := Detect(s)

	d, ok := find(report, CodeDaemonUnverified)
	if !ok {
		t.Fatal("unverified daemon not detected")
	}
	if d.Repairable {
		t.Error("an unverified daemon must not be repaired automatically")
	}
	if report.Health != HealthBlocked {
		t.Errorf("health = %q, want blocked", report.Health)
	}
	if report.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2", report.ExitCode())
	}
}

func TestStaleSocketIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.serverAlive = false
	s.socket = true
	s.daemonState = "stopped"

	report := Detect(s)

	d, ok := find(report, CodeSocketStale)
	if !ok {
		t.Fatal("stale socket not detected")
	}
	if !d.Repairable {
		t.Error("a stale socket should be repairable")
	}

	// With no server, per-role session noise is suppressed.
	if _, ok := find(report, CodeSessionMissing); ok {
		t.Error("session diagnostics were reported with no tmux server at all")
	}
}

func TestMissingSessionIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.sessions["refactorer"] = false

	report := Detect(s)

	d, ok := find(report, CodeSessionMissing)
	if !ok {
		t.Fatal("missing session not detected")
	}
	if d.Component != "refactorer" || !d.Repairable {
		t.Errorf("diagnostic = %+v", d)
	}

	// The healthy roles produced nothing.
	for _, other := range report.Diagnostics {
		if other.Component == "coder" {
			t.Errorf("a healthy role produced %+v", other)
		}
	}
}

func TestMissingAgentIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.agents["coder"] = "not-started"

	report := Detect(s)

	d, ok := find(report, CodeAgentMissing)
	if !ok {
		t.Fatal("missing agent not detected")
	}
	if d.Component != "coder" || !d.Repairable {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestWorktreeStates(t *testing.T) {
	cases := []struct {
		state      WorktreeState
		wantCode   string
		repairable bool
	}{
		{WorktreeMissing, CodeWorktreeMissing, true},
		{WorktreeDirty, CodeWorktreeDirty, false},
		{WorktreeDetached, CodeWorktreeDetached, false},
		{WorktreeWrongRef, CodeWorktreeBranch, false},
		{WorktreeInvalid, CodeWorktreeInvalid, false},
	}

	for _, c := range cases {
		s := newFakeSystem()
		wt := s.worktrees["coder"]
		wt.State = c.state
		wt.Registered = false // a missing-but-unregistered worktree is safe to recreate
		s.worktrees["coder"] = wt

		report := Detect(s)

		d, ok := find(report, c.wantCode)
		if !ok {
			t.Errorf("%s: not detected (%+v)", c.state, report.Diagnostics)
			continue
		}
		if d.Repairable != c.repairable {
			t.Errorf("%s: repairable = %v, want %v", c.state, d.Repairable, c.repairable)
		}
	}
}

// A dirty worktree must never be presented as something repair can fix.
func TestDirtyWorktreeIsNeverRepairable(t *testing.T) {
	s := newFakeSystem()
	wt := s.worktrees["coder"]
	wt.State = WorktreeDirty
	s.worktrees["coder"] = wt

	report := Detect(s)

	d, ok := find(report, CodeWorktreeDirty)
	if !ok {
		t.Fatal("dirty worktree not detected")
	}
	if d.Repairable {
		t.Fatal("a dirty worktree was marked repairable; repair could destroy work")
	}
	for _, r := range report.Repairable() {
		if r.Component == "coder" && r.Code == CodeWorktreeDirty {
			t.Fatal("dirty worktree leaked into the repairable set")
		}
	}
}

// A missing worktree that Git still has registered is ambiguous.
func TestRegisteredMissingWorktreeIsNotAutoRepaired(t *testing.T) {
	s := newFakeSystem()
	wt := s.worktrees["coder"]
	wt.State = WorktreeMissing
	wt.Registered = true
	s.worktrees["coder"] = wt

	report := Detect(s)

	d, _ := find(report, CodeWorktreeMissing)
	if d.Repairable {
		t.Error("a worktree with conflicting Git metadata was marked repairable")
	}
}

func TestTaskModeCurrentCorruptionIsBlocking(t *testing.T) {
	s := newFakeSystem()
	s.current["coder"] = 2

	report := Detect(s)

	d, ok := find(report, CodeCurrentCorrupt)
	if !ok {
		t.Fatal("current corruption not detected")
	}
	if d.Repairable {
		t.Error("current corruption must not be repaired automatically")
	}
	if report.Health != HealthBlocked {
		t.Errorf("health = %q, want blocked", report.Health)
	}
}

// Batch mode legitimately holds several current items.
func TestBatchModeCurrentIsNotCorruption(t *testing.T) {
	s := newFakeSystem()
	for i := range s.roles {
		if s.roles[i].Name == "refactorer" {
			s.roles[i].ReceiveMode = "batch"
		}
	}
	s.current["refactorer"] = 3

	report := Detect(s)

	if _, ok := find(report, CodeCurrentCorrupt); ok {
		t.Error("a batch role's multiple current items were reported as corruption")
	}
}

func TestOrphanDeliveryIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.orphans = []Orphan{{
		Role: "coder", ID: "abc123def456", Path: "/repo/.swarm/handoffs/coder/outbox/x.handoff",
		Destinations: []string{"refactorer"},
	}}

	report := Detect(s)

	d, ok := find(report, CodeOrphanDelivery)
	if !ok {
		t.Fatal("orphan delivery not detected")
	}
	if !d.Repairable || d.Detail["id"] != "abc123def456" {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestFailedAndRejectedHandoffsAreReported(t *testing.T) {
	s := newFakeSystem()
	s.failed["coder"] = 2
	s.rejected = 1

	report := Detect(s)

	if _, ok := find(report, CodeHandoffFailed); !ok {
		t.Error("failed handoffs not reported")
	}
	if _, ok := find(report, CodeHandoffRejected); !ok {
		t.Error("rejected handoffs not reported")
	}
	// Neither is auto-repairable: retry is an explicit, per-handoff decision.
	for _, d := range report.Repairable() {
		if d.Code == CodeHandoffFailed || d.Code == CodeHandoffRejected {
			t.Errorf("%s was marked repairable", d.Code)
		}
	}
}

func TestTempFilesAreRepairable(t *testing.T) {
	s := newFakeSystem()
	s.tempFiles = []string{"/repo/.swarm/handoffs/coder/outbox/.tmp-123"}

	report := Detect(s)

	d, ok := find(report, CodeTempFiles)
	if !ok {
		t.Fatal("stale temp files not detected")
	}
	if !d.Repairable || d.Severity != SeverityInfo {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestMissingRepositoryStopsInspection(t *testing.T) {
	s := newFakeSystem()
	s.repoErr = fmt.Errorf("not a git repository")

	report := Detect(s)

	if report.Health != HealthBlocked {
		t.Errorf("health = %q, want blocked", report.Health)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != CodeRepoMissing {
		t.Errorf("diagnostics = %+v, want only the repository failure", report.Diagnostics)
	}
}

func TestStoppedSwarmIsNotUnhealthy(t *testing.T) {
	s := newFakeSystem()
	s.daemonState = "stopped"
	s.serverAlive = false
	s.socket = false
	for role := range s.sessions {
		s.sessions[role] = false
	}

	report := Detect(s)

	if report.Running {
		t.Error("a stopped swarm reported as running")
	}
	if report.Health != HealthStopped {
		t.Errorf("health = %q, want stopped", report.Health)
	}
	if report.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0 for an intentionally stopped swarm", report.ExitCode())
	}
}

func TestBackendAndPromptFailures(t *testing.T) {
	s := newFakeSystem()
	s.backends = map[string]bool{}
	s.promptErr["coder"] = fmt.Errorf("prompts/roles/coder.prompt does not exist")

	report := Detect(s)

	if _, ok := find(report, CodeBackendMissing); !ok {
		t.Error("missing backend not detected")
	}
	if _, ok := find(report, CodePromptsMissing); !ok {
		t.Error("missing prompt not detected")
	}
}

func TestReportJSON(t *testing.T) {
	s := newFakeSystem()
	s.sessions["refactorer"] = false

	report := Detect(s)

	data, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Health      string `json:"health"`
		Diagnostics []struct {
			Code       string `json:"code"`
			Severity   string `json:"severity"`
			Component  string `json:"component"`
			Repairable bool   `json:"repairable"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}

	if decoded.Health != "degraded" {
		t.Errorf("health = %q", decoded.Health)
	}
	if len(decoded.Diagnostics) == 0 {
		t.Fatal("no diagnostics in JSON")
	}

	var found bool
	for _, d := range decoded.Diagnostics {
		if d.Code == CodeSessionMissing && d.Component == "refactorer" && d.Repairable {
			found = true
		}
	}
	if !found {
		t.Errorf("JSON lacks the session diagnostic:\n%s", data)
	}
}

// Diagnostics must be ordered worst-first so the output leads with what matters.
func TestDiagnosticsAreSortedBySeverity(t *testing.T) {
	s := newFakeSystem()
	s.tempFiles = []string{"/repo/.swarm/x/.tmp-1"} // info
	s.failed["coder"] = 1                           // warning
	s.sessions["architect"] = false                 // error
	s.current["coder"] = 2                          // critical

	report := Detect(s)

	rank := map[Severity]int{SeverityCritical: 0, SeverityError: 1, SeverityWarning: 2, SeverityInfo: 3}
	for i := 1; i < len(report.Diagnostics); i++ {
		if rank[report.Diagnostics[i-1].Severity] > rank[report.Diagnostics[i].Severity] {
			t.Fatalf("diagnostics are not sorted worst-first: %+v", report.Diagnostics)
		}
	}
}

// A handed-off commit that has not been applied yet is repairable work.
func TestPendingIntegrationIsDetected(t *testing.T) {
	s := newFakeSystem()
	s.integration["refactorer"] = [2]string{"pending", ""}

	report := Detect(s)

	d, ok := find(report, CodeIntegrationPend)
	if !ok {
		t.Fatalf("pending integration not detected: %+v", report.Diagnostics)
	}
	if d.Component != "refactorer" || !d.Repairable {
		t.Errorf("diagnostic = %+v", d)
	}
}

// A conflict is never something repair should resolve.
func TestFailedIntegrationIsBlocking(t *testing.T) {
	s := newFakeSystem()
	s.integration["refactorer"] = [2]string{"failed", "cherry-pick conflict: shared.txt"}

	report := Detect(s)

	d, ok := find(report, CodeIntegrationFail)
	if !ok {
		t.Fatalf("failed integration not detected: %+v", report.Diagnostics)
	}
	if d.Repairable {
		t.Fatal("a conflicted integration was marked repairable")
	}
	if report.Health != HealthBlocked {
		t.Errorf("health = %q, want blocked", report.Health)
	}
	if d.Detail["reason"] == "" {
		t.Error("the conflict reason was not carried through")
	}
}
