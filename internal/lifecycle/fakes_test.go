package lifecycle

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The fakes below stand in for tmux, Git and the AI backend so the
// orchestration itself can be tested deterministically.

type fakeWorktrees struct {
	mu      sync.Mutex
	present map[string]bool
	failOn  map[string]error
	created []string
}

func newFakeWorktrees() *fakeWorktrees {
	return &fakeWorktrees{present: map[string]bool{}, failOn: map[string]error{}}
}

func (f *fakeWorktrees) Ensure(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failOn[r.Name]; err != nil {
		return false, err
	}
	if f.present[r.Name] {
		return false, nil
	}

	f.present[r.Name] = true
	f.created = append(f.created, r.Name)

	return true, nil
}

func (f *fakeWorktrees) Present(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present[r.Name], nil
}

type fakeSessions struct {
	mu      sync.Mutex
	present map[string]bool
	failOn  map[string]error
	created []string
	removed []string
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{present: map[string]bool{}, failOn: map[string]error{}}
}

func (f *fakeSessions) Ensure(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failOn[r.Name]; err != nil {
		return false, err
	}
	if f.present[r.Name] {
		return false, nil
	}

	f.present[r.Name] = true
	f.created = append(f.created, r.Name)

	return true, nil
}

func (f *fakeSessions) Present(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present[r.Name], nil
}

func (f *fakeSessions) Remove(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.present[r.Name] {
		return false, nil
	}

	delete(f.present, r.Name)
	f.removed = append(f.removed, r.Name)

	return true, nil
}

type fakeAgents struct {
	mu      sync.Mutex
	running map[string]bool
	failOn  map[string]error
	started []string
	stopped []string
}

func newFakeAgents() *fakeAgents {
	return &fakeAgents{running: map[string]bool{}, failOn: map[string]error{}}
}

func (f *fakeAgents) Ensure(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failOn[r.Name]; err != nil {
		return false, err
	}
	if f.running[r.Name] {
		return false, nil
	}

	f.running[r.Name] = true
	f.started = append(f.started, r.Name)

	return true, nil
}

func (f *fakeAgents) Stop(r Role) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.running[r.Name] {
		return false, nil
	}

	delete(f.running, r.Name)
	f.stopped = append(f.stopped, r.Name)

	return true, nil
}

func (f *fakeAgents) State(r Role) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.running[r.Name] {
		return "running", nil
	}
	return "not-started", nil
}

type fakeWork struct {
	states map[string]string
	tasks  map[string]string
	counts Counts
}

func newFakeWork() *fakeWork {
	return &fakeWork{states: map[string]string{}, tasks: map[string]string{}}
}

func (f *fakeWork) Work(role string) (string, string, error) {
	state, ok := f.states[role]
	if !ok {
		state = "waiting"
	}
	return state, f.tasks[role], nil
}

func (f *fakeWork) Counts() (Counts, error) { return f.counts, nil }

type fakeEnv struct {
	tmux     bool
	backends map[string]bool
	prompts  error
	bin      string
	binErr   error
}

func newFakeEnv(bin string) *fakeEnv {
	return &fakeEnv{tmux: true, backends: map[string]bool{"codex": true}, bin: bin}
}

func (f *fakeEnv) TmuxAvailable() bool                  { return f.tmux }
func (f *fakeEnv) BackendAvailable(backend string) bool { return f.backends[backend] }
func (f *fakeEnv) PromptsPresent(role string) error     { return f.prompts }
func (f *fakeEnv) SwarmBinary() (string, error)         { return f.bin, f.binErr }

// testRoles builds the four-pack against a repository root.
func testRoles(repoRoot string) []Role {
	names := []string{"specifier", "coder", "refactorer", "architect"}

	roles := make([]Role, 0, len(names))
	for _, name := range names {
		roles = append(roles, Role{
			Name:         name,
			Backend:      "codex",
			WorktreeName: "wt-" + name,
			Worktree:     filepath.Join(repoRoot, ".swarm", "worktrees", "wt-"+name),
			Branch:       "swarm/" + name,
			ReceiveMode:  "task",
		})
	}

	return roles
}

// newTestManager returns a manager over a fake repository, with the background
// daemon disabled: daemon behavior has its own tests.
func newTestManager(t *testing.T) (*Manager, *fakeWorktrees, *fakeSessions, *fakeAgents, *fakeWork) {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	wt, sessions, agents, work := newFakeWorktrees(), newFakeSessions(), newFakeAgents(), newFakeWork()

	m := &Manager{
		RepoRoot:   root,
		Roles:      testRoles(root),
		Worktrees:  wt,
		Sessions:   sessions,
		Agents:     agents,
		Work:       work,
		Env:        newFakeEnv(filepath.Join(root, "bin", "swarm")),
		Out:        io.Discard,
		SkipDaemon: true,
	}

	return m, wt, sessions, agents, work
}

// stepNames flattens a report's steps for order assertions.
func stepNames(steps []Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

// indexOf reports where a step appears, or -1.
func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

func mustNoError(t *testing.T, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", context, err)
	}
}

var errBoom = fmt.Errorf("boom")
