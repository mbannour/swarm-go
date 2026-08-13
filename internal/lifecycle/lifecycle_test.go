package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightSucceeds(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)

	for _, c := range m.Preflight() {
		if !c.OK() {
			t.Errorf("check %q failed: %v", c.Name, c.Err)
		}
	}
}

func TestPreflightFailsWithoutTmux(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)
	m.Env.(*fakeEnv).tmux = false

	if !hasFailedCheck(m.Preflight(), "tmux") {
		t.Error("preflight passed with tmux missing")
	}

	if _, err := m.Start(context.Background()); err == nil {
		t.Error("Start succeeded with tmux missing")
	}
}

func TestPreflightFailsWithoutBackend(t *testing.T) {
	m, _, _, agents, _ := newTestManager(t)
	m.Env.(*fakeEnv).backends = map[string]bool{}

	if !hasFailedCheck(m.Preflight(), "agent backends") {
		t.Error("preflight passed with the backend missing")
	}

	// Nothing may have been started.
	if _, err := m.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with the backend missing")
	}
	if len(agents.started) != 0 {
		t.Errorf("agents were started despite a failed preflight: %v", agents.started)
	}
}

func TestPreflightFailsWithoutPrompts(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)
	m.Env.(*fakeEnv).prompts = errBoom

	if !hasFailedCheck(m.Preflight(), "prompts") {
		t.Error("preflight passed with prompts missing")
	}
}

func TestPreflightRejectsWorktreeOutsideRepo(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)
	m.Roles[1].Worktree = "/etc"

	if !hasFailedCheck(m.Preflight(), "worktree paths") {
		t.Error("preflight accepted a worktree outside the managed area")
	}
}

func TestPreflightRejectsDuplicateRoles(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)
	m.Roles = append(m.Roles, m.Roles[0])

	if !hasFailedCheck(m.Preflight(), "configuration") {
		t.Error("preflight accepted a duplicate role")
	}
}

func hasFailedCheck(checks []Check, name string) bool {
	for _, c := range checks {
		if c.Name == name && !c.OK() {
			return true
		}
	}
	return false
}

// Worktrees must exist before sessions, and sessions before agents.
func TestStartOrder(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)

	report, err := m.Start(context.Background())
	mustNoError(t, err, "Start")

	names := stepNames(report.Steps)

	for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
		wt := indexOf(names, "worktree:"+role)
		session := indexOf(names, "session:"+role)
		agent := indexOf(names, "agent:"+role)

		if wt < 0 || session < 0 || agent < 0 {
			t.Fatalf("missing steps for %s: %v", role, names)
		}
		if !(wt < session && session < agent) {
			t.Errorf("%s order is worktree=%d session=%d agent=%d", role, wt, session, agent)
		}
	}

	// Every worktree precedes every session, because the daemon sits between.
	lastWorktree, firstSession := -1, len(names)
	for i, n := range names {
		if strings.HasPrefix(n, "worktree:") && i > lastWorktree {
			lastWorktree = i
		}
		if strings.HasPrefix(n, "session:") && i < firstSession {
			firstSession = i
		}
	}
	if lastWorktree > firstSession {
		t.Errorf("a session was created before the last worktree: %v", names)
	}
}

func TestStartBringsEverythingUp(t *testing.T) {
	m, wt, sessions, agents, _ := newTestManager(t)

	report, err := m.Start(context.Background())
	mustNoError(t, err, "Start")

	if !report.Complete {
		t.Error("report is not complete")
	}
	if report.AlreadyUp {
		t.Error("a cold start reported the swarm as already up")
	}
	if len(wt.created) != 4 || len(sessions.created) != 4 || len(agents.started) != 4 {
		t.Errorf("created %d worktrees, %d sessions, %d agents", len(wt.created), len(sessions.created), len(agents.started))
	}
	if report.Status.Health != HealthDegraded && report.Status.Health != HealthHealthy {
		t.Errorf("health = %q", report.Status.Health)
	}
}

// A second start must repair, not duplicate.
func TestStartIsIdempotent(t *testing.T) {
	m, wt, sessions, agents, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	report, err := m.Start(context.Background())
	mustNoError(t, err, "second Start")

	if len(wt.created) != 4 || len(sessions.created) != 4 || len(agents.started) != 4 {
		t.Errorf("second start duplicated components: worktrees=%v sessions=%v agents=%v",
			wt.created, sessions.created, agents.started)
	}

	for _, s := range report.Steps {
		if s.Created {
			t.Errorf("step %q reported as newly created on the second start", s.Name)
		}
	}
}

// A component that disappeared is restored by the next start.
func TestStartRepairsMissingComponent(t *testing.T) {
	m, _, sessions, agents, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The coder's session dies.
	delete(sessions.present, "coder")
	delete(agents.running, "coder")

	_, err := m.Start(context.Background())
	mustNoError(t, err, "repair Start")

	if !sessions.present["coder"] || !agents.running["coder"] {
		t.Fatal("the missing coder was not restored")
	}

	// Only the coder was touched.
	if len(sessions.created) != 5 {
		t.Errorf("sessions created = %v, want one extra for coder", sessions.created)
	}
}

// Policy B: keep what worked, report honestly, fail loudly.
func TestPartialStartupIsReportedAndFails(t *testing.T) {
	m, _, sessions, agents, _ := newTestManager(t)
	sessions.failOn["refactorer"] = errBoom

	report, err := m.Start(context.Background())
	if err == nil {
		t.Fatal("Start reported success despite a failed component")
	}
	if report.Complete {
		t.Error("report claims completeness")
	}

	// The other three roles are up and were not rolled back.
	for _, role := range []string{"specifier", "coder", "architect"} {
		if !agents.running[role] {
			t.Errorf("%s was rolled back after an unrelated failure", role)
		}
	}

	// The failure is attributed to the right component, and no agent was
	// started for the role whose session failed.
	var found bool
	for _, s := range report.Steps {
		if s.Name == "session:refactorer" && s.Err != nil {
			found = true
		}
		if s.Name == "agent:refactorer" {
			t.Error("an agent was started for a role with no session")
		}
	}
	if !found {
		t.Error("the failing step was not reported")
	}

	if report.Status.Health == HealthHealthy {
		t.Error("a partially started swarm reported healthy")
	}
}

func TestStopOrder(t *testing.T) {
	m, _, _, _, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	report, err := m.Stop(context.Background())
	mustNoError(t, err, "Stop")

	names := stepNames(report.Steps)

	// Agents stop before sessions are removed, for every role.
	lastAgent, firstSession := -1, len(names)
	for i, n := range names {
		if strings.HasPrefix(n, "agent:") && i > lastAgent {
			lastAgent = i
		}
		if strings.HasPrefix(n, "session:") && i < firstSession {
			firstSession = i
		}
	}
	if lastAgent > firstSession {
		t.Errorf("a session was removed before the last agent stopped: %v", names)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	m, _, sessions, agents, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	report, err := m.Stop(context.Background())
	mustNoError(t, err, "second Stop")

	if !report.AlreadyOff {
		t.Error("second stop did not report the swarm as already stopped")
	}
	if len(sessions.removed) != 4 || len(agents.stopped) != 4 {
		t.Errorf("second stop acted again: sessions=%v agents=%v", sessions.removed, agents.stopped)
	}
}

// Stop is not cleanup: durable state must survive untouched.
func TestStopPreservesDurableState(t *testing.T) {
	m, wt, _, _, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Stand in for durable handoff state on disk.
	handoffDir := filepath.Join(m.RepoRoot, ".swarm", "handoffs", "coder", "current")
	if err := os.MkdirAll(handoffDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(handoffDir, "AUTH-42.handoff")
	if err := os.WriteFile(marker, []byte("type: note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("stop deleted durable handoff state: %v", err)
	}

	// Worktrees are untouched by stop.
	for _, r := range m.Roles {
		present, err := wt.Present(r)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			t.Errorf("stop removed the worktree for %s", r.Name)
		}
	}
}

// Current work must still be there after a stop/start cycle.
func TestCurrentWorkSurvivesStopStart(t *testing.T) {
	m, _, _, _, work := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	work.states["coder"] = "working"
	work.tasks["coder"] = "AUTH-42"

	if _, err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	report, err := m.Start(context.Background())
	mustNoError(t, err, "restart")

	for _, r := range report.Status.Roles {
		if r.Role == "coder" {
			if r.Task != "AUTH-42" || r.Work != "working" {
				t.Errorf("coder resumed as %+v, want AUTH-42 still current", r)
			}
			return
		}
	}
	t.Fatal("coder missing from status")
}

// Status must never change anything.
func TestStatusIsReadOnly(t *testing.T) {
	m, wt, sessions, agents, _ := newTestManager(t)

	status, err := m.Status(context.Background())
	mustNoError(t, err, "Status")

	if status.Running() {
		t.Error("status reported a stopped swarm as running")
	}
	if status.Health != HealthStopped {
		t.Errorf("health = %q, want stopped", status.Health)
	}
	if len(wt.created) != 0 || len(sessions.created) != 0 || len(agents.started) != 0 {
		t.Error("status created components")
	}

	// It must not even create the runtime directory.
	if _, err := os.Stat(filepath.Join(m.RepoRoot, ".swarm", "runtime")); err == nil {
		t.Error("status created the runtime directory")
	}
}

func TestStatusDetectsMissingSessionAndAgent(t *testing.T) {
	m, _, sessions, agents, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	delete(sessions.present, "coder")
	delete(agents.running, "refactorer")

	status, err := m.Status(context.Background())
	mustNoError(t, err, "Status")

	for _, r := range status.Roles {
		switch r.Role {
		case "coder":
			if r.Session != StateMissing {
				t.Errorf("coder session = %q, want missing", r.Session)
			}
		case "refactorer":
			if r.Agent != StateStopped {
				t.Errorf("refactorer agent = %q, want stopped", r.Agent)
			}
		}
	}

	if status.Health != HealthDegraded {
		t.Errorf("health = %q, want degraded", status.Health)
	}
}

func TestStatusReportsCurrentTask(t *testing.T) {
	m, _, _, _, work := newTestManager(t)
	work.states["coder"] = "working"
	work.tasks["coder"] = "AUTH-42"
	work.counts = Counts{Inbox: 2, Current: 1}

	status, err := m.Status(context.Background())
	mustNoError(t, err, "Status")

	var found bool
	for _, r := range status.Roles {
		if r.Role == "coder" {
			found = r.Task == "AUTH-42" && r.Work == "working"
		}
	}
	if !found {
		t.Errorf("current task missing from status: %+v", status.Roles)
	}
	if status.Handoffs.Inbox != 2 || status.Handoffs.Current != 1 {
		t.Errorf("counts = %+v", status.Handoffs)
	}
}

func TestStatusJSON(t *testing.T) {
	m, _, _, _, work := newTestManager(t)
	work.states["coder"] = "working"
	work.tasks["coder"] = "AUTH-42"

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	status, err := m.Status(context.Background())
	mustNoError(t, err, "Status")

	data, err := status.JSON()
	mustNoError(t, err, "JSON")

	// Decode into a generic map: the wire shape is the contract.
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("status JSON is not valid JSON: %v\n%s", err, data)
	}

	for _, key := range []string{"health", "daemon", "roles", "handoffs", "repository"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("JSON is missing %q:\n%s", key, data)
		}
	}

	roles, ok := decoded["roles"].([]interface{})
	if !ok || len(roles) != 4 {
		t.Fatalf("roles = %v", decoded["roles"])
	}

	first, ok := roles[0].(map[string]interface{})
	if !ok {
		t.Fatalf("role entry = %v", roles[0])
	}
	for _, key := range []string{"name", "session", "agent", "work"} {
		if _, ok := first[key]; !ok {
			t.Errorf("role JSON is missing %q: %v", key, first)
		}
	}

	daemon, ok := decoded["daemon"].(map[string]interface{})
	if !ok || daemon["status"] == nil {
		t.Errorf("daemon JSON = %v", decoded["daemon"])
	}
}

// Two repositories must not share runtime identity.
func TestRepositoriesAreIndependent(t *testing.T) {
	a, _, _, agentsA, _ := newTestManager(t)
	b, _, _, agentsB, _ := newTestManager(t)

	if a.RepoRoot == b.RepoRoot {
		t.Fatal("test repositories share a root")
	}

	for _, path := range [][2]string{
		{DaemonLockPath(a.RepoRoot), DaemonLockPath(b.RepoRoot)},
		{DaemonPIDPath(a.RepoRoot), DaemonPIDPath(b.RepoRoot)},
		{LifecycleLockPath(a.RepoRoot), LifecycleLockPath(b.RepoRoot)},
		{DaemonLogPath(a.RepoRoot), DaemonLogPath(b.RepoRoot)},
	} {
		if path[0] == path[1] {
			t.Errorf("repositories share runtime path %s", path[0])
		}
	}

	if _, err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Stopping A must leave B alone.
	if _, err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(agentsA.stopped) != 4 {
		t.Errorf("repository A did not stop: %v", agentsA.stopped)
	}
	if len(agentsB.stopped) != 0 {
		t.Errorf("stopping A stopped B: %v", agentsB.stopped)
	}

	statusB, err := b.Status(context.Background())
	mustNoError(t, err, "B status")
	if !statusB.Running() {
		t.Error("repository B stopped when A did")
	}
}

func TestMetadataIsNotAuthoritative(t *testing.T) {
	m, _, sessions, agents, _ := newTestManager(t)

	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Metadata still says everything started, but reality changed.
	for _, r := range m.Roles {
		delete(sessions.present, r.Name)
		delete(agents.running, r.Name)
	}

	status, err := m.Status(context.Background())
	mustNoError(t, err, "Status")

	if status.Running() {
		t.Error("status trusted stale metadata over live observation")
	}
	if status.StartedAt == "" {
		t.Error("metadata was not recorded at all")
	}
}
