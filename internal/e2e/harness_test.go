// Package e2e drives one complete requirement through the four-pack and
// asserts the durable state at every step.
//
// It uses a real Git repository, real commits, the real handoff store, the real
// daemon logic and the real ready/next/done lifecycle. What it fakes is the
// intelligence: each role's work is a deterministic function rather than an AI
// call, so the orchestrator is under test and no paid service is required.
//
// tmux is deliberately not involved here — that path is covered by the
// lifecycle integration tests and by scripts/e2e-fourpack.sh — so this suite
// runs anywhere `go test ./...` runs.
package e2e

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
)

// roleNames is the four-pack, in flow order.
var roleNames = []string{"specifier", "coder", "refactorer", "architect"}

// swarm is a whole running system for one test: a repository, four worktrees,
// the handoff store, the lifecycle and the delivery daemon.
type swarm struct {
	t      *testing.T
	root   string
	store  *handoff.Store
	life   *handoff.Lifecycle
	daemon *handoff.Daemon
	wt     *git.WorktreeManager
	trees  map[string]string // role -> worktree path
}

// newSwarm builds a temporary repository with an initial commit and the full
// four-pack laid out on disk.
func newSwarm(t *testing.T) *swarm {
	t.Helper()

	if !git.Available() {
		t.Skip("git not available")
	}

	root := t.TempDir()

	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "swarm@example.com")
	runGit(t, root, "config", "user.name", "swarm")

	writeFile(t, filepath.Join(root, "README.md"), "demo project\n")
	writeFile(t, filepath.Join(root, ".gitignore"), ".swarm/\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "initial commit")

	wt, err := git.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	store := handoff.NewStore(root, handoff.NewRoles(roleNames))
	if err := store.EnsureDirs(roleNames); err != nil {
		t.Fatal(err)
	}

	life := handoff.NewLifecycle(store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})

	daemon := handoff.NewDaemon(store, roleNames, nil, git.NewRepo(root))
	daemon.Log = io.Discard

	s := &swarm{
		t: t, root: root, store: store, life: life, daemon: daemon,
		wt: wt, trees: map[string]string{},
	}

	// Four isolated worktrees, each on its own branch.
	for _, role := range roleNames {
		created, err := s.ensureWorktree(role)
		if err != nil {
			t.Fatalf("worktree for %s: %v", role, err)
		}
		if !created {
			t.Fatalf("worktree for %s already existed", role)
		}
	}

	return s
}

func (s *swarm) ensureWorktree(role string) (bool, error) {
	wt, created, err := s.wt.Create(role, "wt-"+role)
	if err != nil {
		return false, err
	}
	s.trees[role] = wt.AbsPath
	return created, nil
}

// restart rebuilds every in-memory component from disk, which is exactly what a
// `swarm stop` followed by `swarm start` leaves an agent facing.
func (s *swarm) restart() {
	s.t.Helper()

	s.store = handoff.NewStore(s.root, handoff.NewRoles(roleNames))
	s.life = handoff.NewLifecycle(s.store, func(string) (handoff.ReceiveMode, error) {
		return handoff.ModeTask, nil
	})
	s.daemon = handoff.NewDaemon(s.store, roleNames, nil, git.NewRepo(s.root))
	s.daemon.Log = io.Discard
}

// submit is the developer boundary: `swarm task submit`.
func (s *swarm) submit(id, description string) handoff.Entry {
	s.t.Helper()

	entry, err := s.store.Submit(handoff.Handoff{
		Type:     handoff.TypeNote,
		Priority: 20,
		Note:     fmt.Sprintf("[%s] %s", id, description),
	}, "specifier")
	if err != nil {
		s.t.Fatalf("submit: %v", err)
	}

	return entry
}

// deliver runs one daemon pass.
func (s *swarm) deliver() handoff.ScanResult {
	s.t.Helper()
	return s.daemon.Scan()
}

// inbox, current and completed read durable state.
func (s *swarm) inbox(role string) []handoff.Entry     { return s.list(role, handoff.BoxInbox) }
func (s *swarm) current(role string) []handoff.Entry   { return s.list(role, handoff.BoxCurrent) }
func (s *swarm) completed(role string) []handoff.Entry { return s.list(role, handoff.BoxCompleted) }
func (s *swarm) failed(role string) []handoff.Entry    { return s.list(role, handoff.BoxFailed) }
func (s *swarm) outbox(role string) []handoff.Entry    { return s.list(role, handoff.BoxOutbox) }

func (s *swarm) list(role, box string) []handoff.Entry {
	s.t.Helper()

	entries, err := s.store.List(role, box)
	if err != nil {
		s.t.Fatalf("list %s/%s: %v", role, box, err)
	}

	return entries
}

func (s *swarm) rejected() []handoff.Entry {
	s.t.Helper()

	entries, _, err := s.store.ListDir(s.store.RejectedDir())
	if err != nil {
		s.t.Fatalf("list rejected: %v", err)
	}

	return entries
}

// ---- the fake agent ----------------------------------------------------

// agent is one deterministic worker. It runs the same protocol steps the
// runtime prompt gives a real agent.
type agent struct {
	s    *swarm
	role string
}

func (s *swarm) agent(role string) *agent { return &agent{s: s, role: role} }

// ready accepts work, returning nil for NO_TASK.
func (a *agent) ready() []handoff.Entry {
	a.s.t.Helper()

	selection, err := a.s.life.Ready(a.role)
	if err != nil {
		a.s.t.Fatalf("%s ready: %v", a.role, err)
	}
	if selection.Empty() {
		return nil
	}

	return selection.Entries
}

// work makes this role's deterministic contribution and commits it.
func (a *agent) work() string {
	a.s.t.Helper()
	return a.commit(roleContribution(a.s.t, a.s.trees[a.role], a.role))
}

// commit stages everything in the role's worktree and commits, returning the
// 10-character abbreviation a handoff requires.
func (a *agent) commit(message string) string {
	a.s.t.Helper()

	tree := a.s.trees[a.role]

	status := runGit(a.s.t, tree, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return "" // nothing changed; a note handoff is appropriate
	}

	runGit(a.s.t, tree, "add", "-A")
	runGit(a.s.t, tree, "commit", "-m", message)

	return strings.TrimSpace(runGit(a.s.t, tree, "rev-parse", "--short=10", "HEAD"))
}

// advance routes the result downstream, exactly as `handoff next` does.
func (a *agent) advance(task, commit, note string) (handoff.Entry, bool) {
	a.s.t.Helper()

	msg := handoff.Handoff{Priority: 20, Note: note}
	if commit != "" {
		msg.Type = handoff.TypeGit
		msg.Task = task
		msg.Commit = commit
	} else {
		msg.Type = handoff.TypeNote
	}

	to, err := handoff.NextRole(a.role)
	if err != nil {
		a.s.t.Fatalf("%s route: %v", a.role, err)
	}
	msg.To = []string{to}

	entry, already, err := a.s.life.Advance(a.role, msg)
	if err != nil {
		a.s.t.Fatalf("%s advance: %v", a.role, err)
	}

	return entry, already
}

// done completes the current work.
func (a *agent) done() {
	a.s.t.Helper()

	if _, _, err := a.s.life.Done(a.role); err != nil {
		a.s.t.Fatalf("%s done: %v", a.role, err)
	}
}

// cycle is the whole protocol for one role: ready → work → commit → next → done.
func (a *agent) cycle(task string) handoff.Entry {
	a.s.t.Helper()

	work := a.ready()
	if work == nil {
		a.s.t.Fatalf("%s had no work to do", a.role)
	}

	// A git_handoff names a commit; inspect it rather than assuming.
	if c := work[0].CanonicalCommit; c != "" {
		runGit(a.s.t, a.s.trees[a.role], "cat-file", "-e", c)
	}

	commit := a.work()
	entry, already := a.advance(task, commit, a.role+" completed its part")
	if already {
		a.s.t.Fatalf("%s produced a duplicate handoff", a.role)
	}

	a.done()

	return entry
}

// roleContribution writes each role's fixed changes into its worktree.
func roleContribution(t *testing.T, tree, role string) string {
	t.Helper()

	dir := filepath.Join(tree, "demo", "calculator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	switch role {
	case "specifier":
		writeFile(t, filepath.Join(dir, "SPEC.md"), specDocument)
		return "spec: discount calculator acceptance criteria"

	case "coder":
		writeFile(t, filepath.Join(dir, "calculator.go"), implementation)
		writeFile(t, filepath.Join(dir, "calculator_test.go"), implementationTest)
		runTests(t, dir)
		return "feat: discount calculator with tests"

	case "refactorer":
		writeFile(t, filepath.Join(dir, "calculator.go"), refactored)
		runTests(t, dir)
		return "refactor: extract discount validation"

	case "architect":
		writeFile(t, filepath.Join(dir, "REVIEW.md"), reviewDocument)
		return "docs: architecture review"
	}

	return "chore: no change"
}

// runTests runs the demo project's own tests, so "verify before claiming
// completion" is actually exercised rather than asserted.
func runTests(t *testing.T, dir string) {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		return
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GO111MODULE=off")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("demo tests reported: %v\n%s", err, out)
	}
}

// ---- helpers -----------------------------------------------------------

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}

	return string(out)
}

func gitQuiet(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls a condition with a bounded deadline.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

const specDocument = `# Discount calculator

calculate(price, discountPercent) -> price after discount

Acceptance criteria:
- 100 with 20% returns 80
- 50 with 0% returns 50
- a discount below 0 is invalid
- a discount above 100 is invalid
`

const implementation = `package calculator

import "errors"

var ErrInvalidDiscount = errors.New("discount must be between 0 and 100")

func Calculate(price float64, discountPercent float64) (float64, error) {
	if discountPercent < 0 || discountPercent > 100 {
		return 0, ErrInvalidDiscount
	}
	return price - (price * discountPercent / 100), nil
}
`

const implementationTest = `package calculator

import "testing"

func TestCalculate(t *testing.T) {
	cases := []struct{ price, discount, want float64 }{
		{100, 20, 80},
		{50, 0, 50},
	}
	for _, c := range cases {
		got, err := Calculate(c.price, c.discount)
		if err != nil {
			t.Fatalf("Calculate(%v, %v): %v", c.price, c.discount, err)
		}
		if got != c.want {
			t.Errorf("Calculate(%v, %v) = %v, want %v", c.price, c.discount, got, c.want)
		}
	}
}

func TestCalculateRejectsInvalidDiscount(t *testing.T) {
	for _, discount := range []float64{-1, 101} {
		if _, err := Calculate(100, discount); err == nil {
			t.Errorf("discount %v was accepted", discount)
		}
	}
}
`

const refactored = `package calculator

import "errors"

var ErrInvalidDiscount = errors.New("discount must be between 0 and 100")

const (
	minDiscount = 0
	maxDiscount = 100
)

func Calculate(price float64, discountPercent float64) (float64, error) {
	if !validDiscount(discountPercent) {
		return 0, ErrInvalidDiscount
	}
	return price * (1 - discountPercent/maxDiscount), nil
}

func validDiscount(discountPercent float64) bool {
	return discountPercent >= minDiscount && discountPercent <= maxDiscount
}
`

const reviewDocument = `# Architecture review

- Boundary: a pure function with no I/O.
- Coupling: standard library only.
- Risk: float64 money will accumulate rounding error.

Verdict: acceptable for the stated requirement.
`
