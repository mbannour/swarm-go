package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionName(t *testing.T) {
	cases := map[string]string{
		"specifier":  "swarm-specifier",
		"coder":      "swarm-coder",
		"refactorer": "swarm-refactorer",
		"architect":  "swarm-architect",
	}

	for role, want := range cases {
		if got := SessionName(role); got != want {
			t.Errorf("SessionName(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestProjectIDIsDeterministic(t *testing.T) {
	a := ProjectID("/home/dev/swarm-go")

	if a != ProjectID("/home/dev/swarm-go") {
		t.Error("ProjectID is not stable across calls")
	}
	if a != ProjectID("/home/dev/swarm-go/") {
		t.Error("ProjectID should ignore a trailing separator")
	}
	if a == ProjectID("/home/dev/other-repo") {
		t.Error("different repositories share a ProjectID")
	}
	if len(a) != projectIDLen {
		t.Errorf("ProjectID length = %d, want %d", len(a), projectIDLen)
	}
	if strings.ContainsAny(a, `/\.`) {
		t.Errorf("ProjectID %q is not a safe path segment", a)
	}
}

func TestSocketPath(t *testing.T) {
	repo := "/home/dev/swarm-go"
	got := SocketPath(repo)

	if got != SocketPath(repo) {
		t.Error("SocketPath is not deterministic")
	}
	if got == SocketPath("/home/dev/other-repo") {
		t.Error("different repositories share a socket")
	}
	if !strings.HasSuffix(got, ProjectID(repo)+".sock") {
		t.Errorf("SocketPath = %q, want it to end in <project-id>.sock", got)
	}
	// The socket must never live inside the repository or a worktree.
	if strings.HasPrefix(got, repo) {
		t.Errorf("SocketPath %q is inside the repository", got)
	}
	if !strings.Contains(filepath.Dir(got), "swarm-go-") {
		t.Errorf("SocketPath dir = %q, want a per-user swarm-go- directory", filepath.Dir(got))
	}
}

func TestCreateRequiresExistingWorktree(t *testing.T) {
	if !Available() {
		t.Skip("tmux not available")
	}

	m := &Manager{Socket: filepath.Join(t.TempDir(), "test.sock")}
	missing := filepath.Join(t.TempDir(), "wt-coder")

	_, created, err := m.Create(RoleRef{Name: "coder", WorkingDir: missing})
	if err == nil {
		t.Fatal("expected an error for a missing worktree")
	}
	if created {
		t.Error("session reported as created")
	}
	if !strings.Contains(err.Error(), "swarm worktrees create") {
		t.Errorf("error lacks remediation hint: %v", err)
	}
}

func TestListOnDeadServer(t *testing.T) {
	if !Available() {
		t.Skip("tmux not available")
	}

	// No server has ever run on this socket: everything is simply missing.
	m := &Manager{Socket: filepath.Join(t.TempDir(), "test.sock")}

	statuses, err := m.List([]RoleRef{{Name: "coder", WorkingDir: "/tmp"}})
	if err != nil {
		t.Fatalf("List on a dead server: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Running {
		t.Fatalf("List = %+v, want one non-running row", statuses)
	}
}

// TestSessionLifecycle runs against a throwaway socket, never the developer's
// default tmux server.
func TestSessionLifecycle(t *testing.T) {
	if !Available() {
		t.Skip("tmux not available")
	}

	socket := filepath.Join(t.TempDir(), "test.sock")
	m := &Manager{Socket: socket}

	workdir := t.TempDir()
	ref := RoleRef{Name: "coder", WorkingDir: workdir}

	t.Cleanup(func() {
		_, _ = run(socket, "kill-server")
	})

	s, created, err := m.Create(ref)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created || s.Name != "swarm-coder" {
		t.Fatalf("Create = (%q, %v)", s.Name, created)
	}

	// Idempotent second run.
	if _, created, err := m.Create(ref); err != nil || created {
		t.Fatalf("second Create = (%v, %v), want (false, nil)", created, err)
	}

	statuses, err := m.List([]RoleRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	if !statuses[0].Running {
		t.Error("session not reported as running")
	}

	// The session must have started inside the worktree.
	got, err := run(socket, "display-message", "-p", "-t", "swarm-coder", "#{pane_current_path}")
	if err != nil {
		t.Fatal(err)
	}
	if resolve(got) != resolve(workdir) {
		t.Errorf("session cwd = %q, want %q", got, workdir)
	}

	// Attaching to a role with no session must fail cleanly.
	err = m.Attach(RoleRef{Name: "architect", WorkingDir: workdir})
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Errorf("Attach on a missing session = %v", err)
	}

	if _, removed, err := m.Remove(ref); err != nil || !removed {
		t.Fatalf("Remove = (%v, %v)", removed, err)
	}

	// Removing again is a no-op.
	if _, removed, err := m.Remove(ref); err != nil || removed {
		t.Fatalf("second Remove = (%v, %v), want (false, nil)", removed, err)
	}
}

func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

func TestEnsureSocketDirIsPrivate(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nested", "dir", "test.sock")

	if err := ensureSocketDir(socket); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir perm = %o, want 700", perm)
	}
}

// A wake-up must actually be submitted, not merely typed into the pane.
//
// This is a regression test for a real failure: the daemon sent the text and
// Enter back-to-back, the interactive agent kept the line in its composer
// unsent, and every delivery went unnoticed.
func TestSendPromptSubmitsTheLine(t *testing.T) {
	if !Available() {
		t.Skip("tmux not available")
	}

	socket := filepath.Join(t.TempDir(), "test.sock")
	m := &Manager{Socket: socket}

	workdir := t.TempDir()
	ref := RoleRef{Name: "coder", WorkingDir: workdir}

	t.Cleanup(func() { _, _ = run(socket, "kill-server") })

	if _, _, err := m.Create(ref); err != nil {
		t.Fatal(err)
	}

	// The pane runs a shell, so a submitted line executes and leaves evidence.
	marker := filepath.Join(workdir, "submitted.marker")
	if err := m.SendPrompt(SessionName("coder"), "touch "+marker); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // the line was entered, not just typed
		}
		time.Sleep(50 * time.Millisecond)
	}

	pane, _ := run(socket, "capture-pane", "-p", "-t", SessionName("coder"))
	t.Fatalf("the prompt was never submitted; pane shows:\n%s", pane)
}

// SendKeys stays raw: it types, and the caller decides about Enter.
func TestSendKeysDoesNotSubmitOnItsOwn(t *testing.T) {
	if !Available() {
		t.Skip("tmux not available")
	}

	socket := filepath.Join(t.TempDir(), "test.sock")
	m := &Manager{Socket: socket}

	workdir := t.TempDir()
	t.Cleanup(func() { _, _ = run(socket, "kill-server") })

	if _, _, err := m.Create(RoleRef{Name: "coder", WorkingDir: workdir}); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(workdir, "should-not-exist.marker")
	if err := m.SendKeys(SessionName("coder"), "touch "+marker); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	if _, err := os.Stat(marker); err == nil {
		t.Error("SendKeys submitted the line by itself")
	}
}
