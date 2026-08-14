package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// integrationRepo builds a repository with a main branch and two role
// worktrees, mirroring the real layout.
type integrationRepo struct {
	t     *testing.T
	root  string
	mgr   *WorktreeManager
	trees map[string]string
}

func newIntegrationRepo(t *testing.T) *integrationRepo {
	t.Helper()

	if !Available() {
		t.Skip("git not available")
	}

	root := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "swarm@example.com"},
		{"config", "user.name", "swarm"},
		// Keep line endings byte-for-byte so content assertions are exact,
		// whatever the machine's global core.autocrlf happens to be.
		{"config", "core.autocrlf", "false"},
	} {
		if _, err := run(root, args...); err != nil {
			t.Skipf("git unusable: %v", err)
		}
	}

	r := &integrationRepo{t: t, root: root, trees: map[string]string{}}

	r.write(root, "base.txt", "A\n")
	r.commit(root, "A")

	mgr, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	r.mgr = mgr

	for _, role := range []string{"coder", "refactorer"} {
		wt, _, err := mgr.Create(role, "wt-"+role)
		if err != nil {
			t.Fatalf("worktree for %s: %v", role, err)
		}
		r.trees[role] = wt.AbsPath
	}

	return r
}

func (r *integrationRepo) write(dir, name, body string) {
	r.t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything in dir and commits, returning the new SHA.
func (r *integrationRepo) commit(dir, message string) string {
	r.t.Helper()

	if _, err := run(dir, "add", "-A"); err != nil {
		r.t.Fatal(err)
	}
	if _, err := run(dir, "commit", "-m", message); err != nil {
		r.t.Fatal(err)
	}

	sha, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}

	return sha
}

func (r *integrationRepo) head(dir string) string {
	r.t.Helper()

	sha, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}

	return sha
}

func (r *integrationRepo) fileIn(dir, name string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Scenario A: refactorer is at A, coder is at A→B. Advancing is linear.
func TestIntegrateFastForward(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B: implementation")

	refactorer := r.trees["refactorer"]

	result, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}

	if result.Method != MethodFastForward {
		t.Errorf("method = %q, want fast-forward", result.Method)
	}
	if result.SourceCommit != b || result.LocalCommit != b {
		t.Errorf("a fast-forward must preserve the SHA: source=%s local=%s", result.SourceCommit, result.LocalCommit)
	}
	if r.head(refactorer) != b {
		t.Errorf("refactorer HEAD = %s, want %s", r.head(refactorer), b)
	}

	// The handed-off file is really there.
	if body, ok := r.fileIn(refactorer, "impl.go"); !ok || !strings.Contains(body, "package demo") {
		t.Errorf("the handed-off file is missing from the receiver: %q", body)
	}

	// Still on its own branch.
	branch, err := i.CurrentBranch(refactorer)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "swarm/refactorer" {
		t.Errorf("branch = %q", branch)
	}
}

// Scenario B: both branches moved, touching different files.
func TestIntegrateCherryPick(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B: implementation")

	refactorer := r.trees["refactorer"]
	r.write(refactorer, "notes.md", "refactoring notes\n")
	rHead := r.commit(refactorer, "R: notes")

	result, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}

	if result.Method != MethodCherryPick {
		t.Errorf("method = %q, want cherry-pick", result.Method)
	}
	if result.SourceCommit != b {
		t.Errorf("source commit = %s, want %s", result.SourceCommit, b)
	}
	if result.LocalCommit == b {
		t.Error("a cherry-pick must produce a new local SHA")
	}
	if result.LocalCommit == rHead {
		t.Error("no new commit was created")
	}

	// The receiver has both its own work and the handed-off change.
	if _, ok := r.fileIn(refactorer, "notes.md"); !ok {
		t.Error("the receiver's own work was lost")
	}
	if _, ok := r.fileIn(refactorer, "impl.go"); !ok {
		t.Error("the handed-off change did not arrive")
	}

	// The cherry-picked commit is now an ancestor of HEAD.
	contains, err := i.AlreadyContains(refactorer, result.LocalCommit)
	if err != nil || !contains {
		t.Errorf("local commit is not in the branch: (%v, %v)", contains, err)
	}
}

// Scenario C: both branches changed the same lines.
func TestIntegrateConflictAbortsCleanly(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "shared.txt", "coder version\n")
	b := r.commit(coder, "B: coder edits shared")

	refactorer := r.trees["refactorer"]
	r.write(refactorer, "shared.txt", "refactorer version\n")
	before := r.commit(refactorer, "R: refactorer edits shared")

	_, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err == nil {
		t.Fatal("a conflicting cherry-pick reported success")
	}

	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error is not a ConflictError: %v", err)
	}
	if len(conflict.Files) == 0 || conflict.Files[0] != "shared.txt" {
		t.Errorf("conflicted files = %v, want shared.txt", conflict.Files)
	}

	// The abort left the worktree exactly as it was.
	if head := r.head(refactorer); head != before {
		t.Errorf("HEAD moved during a failed integration: %s -> %s", before, head)
	}

	clean, err := i.IsClean(refactorer)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		status, _ := run(refactorer, "status", "--porcelain")
		t.Errorf("the worktree was left dirty after an aborted cherry-pick:\n%s", status)
	}

	// The receiver's own content is untouched.
	if body, _ := r.fileIn(refactorer, "shared.txt"); body != "refactorer version\n" {
		t.Errorf("the receiver's file was modified: %q", body)
	}

	// And a later, non-conflicting integration still works.
	r.write(coder, "other.txt", "safe\n")
	safe := r.commit(coder, "B2: unrelated file")
	if _, err := i.Integrate(refactorer, "swarm/refactorer", safe); err != nil {
		t.Errorf("the worktree was not usable after the aborted cherry-pick: %v", err)
	}
}

func TestIntegrateIsIdempotent(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B")

	refactorer := r.trees["refactorer"]

	first, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		t.Fatal(err)
	}
	if first.Already {
		t.Error("the first integration reported the commit as already present")
	}

	headAfterFirst := r.head(refactorer)

	second, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already {
		t.Fatal("the second integration applied the commit again")
	}
	if second.Method != MethodNone {
		t.Errorf("method = %q, want none", second.Method)
	}
	if r.head(refactorer) != headAfterFirst {
		t.Error("HEAD moved on a repeated integration")
	}

	// No duplicate commit in the log.
	log, err := run(refactorer, "log", "--oneline")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(log, "B") > 1 {
		t.Errorf("the commit appears more than once:\n%s", log)
	}
}

// The same must hold for a cherry-pick, where SHAs differ.
func TestCherryPickIsIdempotent(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B")

	refactorer := r.trees["refactorer"]
	r.write(refactorer, "notes.md", "notes\n")
	r.commit(refactorer, "R")

	first, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		t.Fatal(err)
	}
	if first.Method != MethodCherryPick {
		t.Fatalf("method = %q", first.Method)
	}

	before := r.head(refactorer)

	// The source commit is not an ancestor after a cherry-pick, so a naive
	// implementation would apply it twice. It must not.
	second, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err != nil {
		// An empty cherry-pick is the expected failure mode if this is not
		// handled; report it as the real problem it is.
		t.Fatalf("repeated cherry-pick failed instead of being detected: %v", err)
	}
	if !second.Already {
		t.Error("the same change was applied twice")
	}
	if r.head(refactorer) != before {
		t.Error("HEAD moved on a repeated cherry-pick")
	}
}

func TestIntegrateRefusesDirtyWorktree(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B")

	refactorer := r.trees["refactorer"]
	r.write(refactorer, "WIP.md", "unsaved work\n")

	_, err := i.Integrate(refactorer, "swarm/refactorer", b)
	if err == nil {
		t.Fatal("integration proceeded into a dirty worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("unhelpful error: %v", err)
	}

	// The uncommitted file is still there, untouched.
	body, ok := r.fileIn(refactorer, "WIP.md")
	if !ok || body != "unsaved work\n" {
		t.Fatalf("uncommitted work was disturbed: %q", body)
	}
}

func TestIntegrateRefusesWrongBranch(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B")

	refactorer := r.trees["refactorer"]
	before := r.head(refactorer)

	_, err := i.Integrate(refactorer, "swarm/somewhere-else", b)
	if err == nil {
		t.Fatal("integration proceeded on an unexpected branch")
	}
	if !strings.Contains(err.Error(), "expected swarm/somewhere-else") {
		t.Errorf("unhelpful error: %v", err)
	}
	if r.head(refactorer) != before {
		t.Error("the worktree was modified despite the branch check")
	}
}

func TestIntegrateRejectsUnknownCommit(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	if _, err := i.Integrate(r.trees["refactorer"], "swarm/refactorer", "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("an unknown commit was accepted")
	}
	if _, err := i.Integrate(r.trees["refactorer"], "swarm/refactorer", ""); err == nil {
		t.Fatal("an empty commit was accepted")
	}
}

func TestAlreadyContainsAndCanFastForward(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	coder := r.trees["coder"]
	r.write(coder, "impl.go", "package demo\n")
	b := r.commit(coder, "B")

	refactorer := r.trees["refactorer"]

	contains, err := i.AlreadyContains(refactorer, b)
	if err != nil || contains {
		t.Errorf("AlreadyContains before integration = (%v, %v)", contains, err)
	}

	canFF, err := i.CanFastForward(refactorer, b)
	if err != nil || !canFF {
		t.Errorf("CanFastForward = (%v, %v), want true", canFF, err)
	}

	if err := i.FastForward(refactorer, b); err != nil {
		t.Fatal(err)
	}

	contains, err = i.AlreadyContains(refactorer, b)
	if err != nil || !contains {
		t.Errorf("AlreadyContains after integration = (%v, %v)", contains, err)
	}

	// Nothing left to fast-forward to.
	canFF, err = i.CanFastForward(refactorer, b)
	if err != nil || canFF {
		t.Errorf("CanFastForward after arriving = (%v, %v), want false", canFF, err)
	}
}

func TestIsCleanAndCurrentBranch(t *testing.T) {
	r := newIntegrationRepo(t)
	i := NewIntegrator()

	tree := r.trees["coder"]

	clean, err := i.IsClean(tree)
	if err != nil || !clean {
		t.Errorf("a fresh worktree reported dirty: (%v, %v)", clean, err)
	}

	r.write(tree, "scratch.txt", "x\n")

	clean, err = i.IsClean(tree)
	if err != nil || clean {
		t.Errorf("a dirty worktree reported clean: (%v, %v)", clean, err)
	}

	branch, err := i.CurrentBranch(tree)
	if err != nil || branch != "swarm/coder" {
		t.Errorf("CurrentBranch = (%q, %v)", branch, err)
	}
}
