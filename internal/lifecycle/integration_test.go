package lifecycle

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mbannour/swarm-go/internal/agent"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// integrationRepo builds a throwaway Git repository with prompts and a
// swarm.conf, ready for a real start/stop cycle.
func integrationRepo(t *testing.T) string {
	t.Helper()

	if !git.Available() {
		t.Skip("git not available")
	}
	if !tmux.Available() {
		t.Skip("tmux not available")
	}

	root := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "swarm@example.com")
	run("config", "user.name", "swarm")

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("README.md", "integration\n")
	write("swarm.conf", strings.Join([]string{
		"window specifier fake wt-specifier task",
		"window coder fake wt-coder task",
		"window refactorer fake wt-refactorer task",
		"window architect fake wt-architect task",
		"",
	}, "\n"))
	write("prompts/constitution.prompt", "constitution\n")
	write("prompts/runtime.prompt", "runtime protocol\n")
	for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
		write("prompts/roles/"+role+".prompt", role+" instructions\n")
	}

	run("add", "-A")
	run("commit", "-m", "initial")

	return root
}

// integrationManager wires real Git, real tmux (on the project's private
// socket) and a fake agent, so the whole pipeline runs without an AI backend.
func integrationManager(t *testing.T, root string) *Manager {
	t.Helper()

	wtMgr, err := git.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	tmuxMgr := tmux.NewManager(root)
	t.Cleanup(func() {
		for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
			_, _, _ = tmuxMgr.Remove(tmux.RoleRef{Name: role, WorkingDir: root})
		}
	})

	roleNames := []string{"specifier", "coder", "refactorer", "architect"}
	store := handoff.NewStore(root, handoff.NewRoles(roleNames))
	if err := store.EnsureDirs(roleNames); err != nil {
		t.Fatal(err)
	}
	life := handoff.NewLifecycle(store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})

	return &Manager{
		RepoRoot:  root,
		Roles:     testRoles(root),
		Worktrees: GitWorktrees{Mgr: wtMgr},
		Sessions:  TmuxSessions{Mgr: tmuxMgr},
		// A fake agent: the real one would launch Codex.
		Agents: &fakeAgents{running: map[string]bool{}, failOn: map[string]error{}},
		Work:   HandoffWork{Store: store, Life: life, Roles: roleNames},
		Env:    newFakeEnv(filepath.Join(root, "bin", "swarm")),
		Out:    io.Discard,
		// The daemon is a separate process with its own tests; here we exercise
		// worktrees, sessions, agents and durable state.
		SkipDaemon: true,
	}
}

// The headline flow: start → observe → stop → durable state survives → restart.
func TestIntegrationStartStopStart(t *testing.T) {
	root := integrationRepo(t)
	m := integrationManager(t, root)

	// ---- start ----
	report, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v\nsteps: %+v", err, report.Steps)
	}

	// Four real worktrees on disk, each with a real branch.
	for _, r := range m.Roles {
		if info, err := os.Stat(r.Worktree); err != nil || !info.IsDir() {
			t.Errorf("worktree missing for %s: %v", r.Name, err)
		}
	}

	// Four real tmux sessions on the project's private socket.
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range status.Roles {
		if r.Session != StateRunning {
			t.Errorf("%s session = %q, want running", r.Role, r.Session)
		}
		if r.Worktree != StateRunning {
			t.Errorf("%s worktree = %q, want present", r.Role, r.Worktree)
		}
		if r.Agent != StateRunning {
			t.Errorf("%s agent = %q, want running", r.Role, r.Agent)
		}
	}

	// Give the coder something to be working on, durably.
	store := handoff.NewStore(root, handoff.NewRoles([]string{"specifier", "coder", "refactorer", "architect"}))
	entry, err := store.Send(handoff.Handoff{
		Type: handoff.TypeNote, From: "specifier", To: []string{"coder"},
		Priority: 20, Note: "Implement rate limiting",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Deliver(entry.Handoff, "coder"); err != nil {
		t.Fatal(err)
	}
	life := handoff.NewLifecycle(store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})
	if _, err := life.Ready("coder"); err != nil {
		t.Fatal(err)
	}

	before, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if workOf(before, "coder") != "working" {
		t.Fatalf("coder work = %q, want working", workOf(before, "coder"))
	}

	// ---- stop ----
	if _, err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Running() {
		t.Error("swarm still running after stop")
	}
	for _, r := range after.Roles {
		if r.Session == StateRunning {
			t.Errorf("%s session survived stop", r.Role)
		}
	}

	// Durable state survives: worktrees and the coder's current work.
	for _, r := range m.Roles {
		if _, err := os.Stat(r.Worktree); err != nil {
			t.Errorf("stop removed the worktree for %s: %v", r.Name, err)
		}
	}
	current, err := store.Current("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 {
		t.Fatalf("current work did not survive stop: %+v", current)
	}

	// ---- start again ----
	if _, err := m.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	resumed, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if workOf(resumed, "coder") != "working" {
		t.Errorf("coder did not resume its work: %q", workOf(resumed, "coder"))
	}
	for _, r := range resumed.Roles {
		if r.Session != StateRunning {
			t.Errorf("%s session did not come back: %q", r.Role, r.Session)
		}
	}

	if _, err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func workOf(s SwarmStatus, role string) string {
	for _, r := range s.Roles {
		if r.Role == role {
			return r.Work
		}
	}
	return ""
}

// Two repositories run side by side without touching each other.
func TestIntegrationTwoRepositoriesAreIndependent(t *testing.T) {
	rootA := integrationRepo(t)
	rootB := integrationRepo(t)

	a := integrationManager(t, rootA)
	b := integrationManager(t, rootB)

	if _, err := a.Start(context.Background()); err != nil {
		t.Fatalf("A start: %v", err)
	}
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatalf("B start: %v", err)
	}

	// Different tmux sockets entirely.
	if tmux.SocketPath(rootA) == tmux.SocketPath(rootB) {
		t.Fatal("both repositories share a tmux socket")
	}

	if _, err := a.Stop(context.Background()); err != nil {
		t.Fatalf("A stop: %v", err)
	}

	statusA, err := a.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := b.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if statusA.Running() {
		t.Error("repository A is still running after its stop")
	}
	if !statusB.Running() {
		t.Error("stopping repository A stopped repository B")
	}
	for _, r := range statusB.Roles {
		if r.Session != StateRunning {
			t.Errorf("B's %s session was killed by A's stop", r.Role)
		}
	}

	if _, err := b.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The real agent binary resolver must refuse a temporary build.
func TestIntegrationBinaryResolutionRefusesTempBuild(t *testing.T) {
	root := t.TempDir()
	t.Setenv(agent.BinaryEnv, "")

	if _, err := (HostEnvironment{RepoRoot: root}).SwarmBinary(); err == nil {
		t.Skip("test binary is not a temporary build")
	}
}
