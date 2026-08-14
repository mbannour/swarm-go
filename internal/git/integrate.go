package git

import (
	"fmt"
	"strings"
)

// Integration methods.
const (
	MethodFastForward = "fast-forward"
	MethodCherryPick  = "cherry-pick"
	MethodNone        = "none" // already present
)

// Integration is the outcome of bringing a handed-off commit into a worktree.
type Integration struct {
	// Method is how the state arrived: fast-forward, cherry-pick, or none.
	Method string
	// SourceCommit is the canonical commit named by the handoff.
	SourceCommit string
	// LocalCommit is what that state is called in the receiving branch. A
	// cherry-pick rewrites the commit, so the two differ; a fast-forward keeps
	// them identical.
	LocalCommit string
	// Already reports that the commit was present before this call.
	Already bool
}

// ConflictError is returned when a cherry-pick cannot be applied cleanly.
//
// The cherry-pick is always aborted before this is returned, so the receiving
// worktree is left clean rather than half-applied.
type ConflictError struct {
	Commit    string
	Worktree  string
	Files     []string
	GitOutput string
}

func (e *ConflictError) Error() string {
	msg := fmt.Sprintf("cherry-pick conflict applying %s", short(e.Commit))
	if len(e.Files) > 0 {
		msg += ": " + strings.Join(e.Files, ", ")
	}
	return msg
}

// Integrator applies handed-off commits to worktrees.
//
// Every Git command runs with the worktree as its working directory, and that
// path always comes from configuration — never from a handoff's contents.
type Integrator struct{}

// NewIntegrator returns an integrator.
func NewIntegrator() *Integrator { return &Integrator{} }

// IsClean reports whether a worktree has no uncommitted changes.
func (i *Integrator) IsClean(worktree string) (bool, error) {
	out, err := run(worktree, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// CurrentBranch returns the branch a worktree has checked out, or "HEAD" when
// it is detached.
func (i *Integrator) CurrentBranch(worktree string) (string, error) {
	return run(worktree, "rev-parse", "--abbrev-ref", "HEAD")
}

// Head returns the worktree's current commit.
func (i *Integrator) Head(worktree string) (string, error) {
	return run(worktree, "rev-parse", "HEAD")
}

// AlreadyContains reports whether commit is an ancestor of the worktree's HEAD,
// which means its state is already present.
func (i *Integrator) AlreadyContains(worktree, commit string) (bool, error) {
	if err := i.exists(worktree, commit); err != nil {
		return false, err
	}

	// --is-ancestor exits 0 for yes and 1 for no; only other codes are errors.
	_, err := run(worktree, "merge-base", "--is-ancestor", commit, "HEAD")
	if err == nil {
		return true, nil
	}

	return false, nil
}

// AlreadyApplied reports whether the *change* a commit carries is already in
// the branch, whether or not that commit's own SHA is.
//
// This matters because a cherry-pick rewrites the commit: the original SHA is
// never an ancestor afterwards, so ancestry alone would happily apply the same
// change a second time. `git cherry` compares patch ids and marks a commit "-"
// when an equivalent change is already upstream.
func (i *Integrator) AlreadyApplied(worktree, commit string) (bool, error) {
	if contains, err := i.AlreadyContains(worktree, commit); err != nil || contains {
		return contains, err
	}

	out, err := run(worktree, "cherry", "HEAD", commit)
	if err != nil {
		// An unrelated history has no comparable patches; treat as not applied.
		return false, nil
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+") {
			return false, nil // at least one change is genuinely new
		}
	}

	// Every line was "-", meaning an equivalent patch is already present.
	return strings.TrimSpace(out) != "", nil
}

// CanFastForward reports whether HEAD is an ancestor of commit, so the branch
// can simply advance to it without rewriting anything.
func (i *Integrator) CanFastForward(worktree, commit string) (bool, error) {
	if err := i.exists(worktree, commit); err != nil {
		return false, err
	}

	head, err := i.Head(worktree)
	if err != nil {
		return false, err
	}
	if head == commit {
		return false, nil // nothing to advance to
	}

	_, err = run(worktree, "merge-base", "--is-ancestor", head, commit)

	return err == nil, nil
}

// FastForward advances the worktree's branch to commit.
func (i *Integrator) FastForward(worktree, commit string) error {
	if _, err := run(worktree, "merge", "--ff-only", "--end-of-options", commit); err != nil {
		return fmt.Errorf("fast-forward to %s: %w", short(commit), err)
	}
	return nil
}

// CherryPick applies commit onto the worktree's branch and returns the new
// local commit.
//
// On conflict the cherry-pick is aborted and a *ConflictError is returned, so
// the caller never has to clean up a partially applied state.
func (i *Integrator) CherryPick(worktree, commit string) (string, error) {
	if _, err := run(worktree, "cherry-pick", "--end-of-options", commit); err != nil {
		conflicted, _ := i.conflictedFiles(worktree)

		// Leave the worktree exactly as it was.
		if _, abortErr := run(worktree, "cherry-pick", "--abort"); abortErr != nil {
			// Abort itself failed: say so plainly rather than pretending the
			// tree is clean.
			return "", fmt.Errorf(
				"cherry-pick of %s failed and could not be aborted (%v); "+
					"the worktree %s needs manual attention: %w",
				short(commit), abortErr, worktree, err)
		}

		return "", &ConflictError{
			Commit: commit, Worktree: worktree,
			Files: conflicted, GitOutput: err.Error(),
		}
	}

	return i.Head(worktree)
}

// conflictedFiles lists the paths a failed cherry-pick left unmerged.
func (i *Integrator) conflictedFiles(worktree string) ([]string, error) {
	out, err := run(worktree, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

// exists verifies that a commit is present in the worktree's repository.
func (i *Integrator) exists(worktree, commit string) error {
	if commit == "" {
		return fmt.Errorf("no commit given")
	}
	if _, err := run(worktree, "rev-parse", "--verify", "--end-of-options", commit+"^{commit}"); err != nil {
		return fmt.Errorf("commit %s is not present in %s", short(commit), worktree)
	}
	return nil
}

// Integrate brings commit into worktree using the safest applicable method.
//
// The strategy, in order:
//
//  1. already an ancestor of HEAD → nothing to do
//  2. HEAD is an ancestor of the commit → fast-forward, keeping history linear
//  3. otherwise → cherry-pick, which rewrites the commit onto this branch
//
// It refuses to touch a dirty worktree or a branch other than the expected one,
// and never resets or discards anything.
func (i *Integrator) Integrate(worktree, expectedBranch, commit string) (Integration, error) {
	result := Integration{SourceCommit: commit}

	// The receiving branch must be the managed one. Integrating into whatever
	// happens to be checked out could scatter work across branches.
	branch, err := i.CurrentBranch(worktree)
	if err != nil {
		return result, err
	}
	if expectedBranch != "" && branch != expectedBranch {
		return result, fmt.Errorf(
			"worktree %s is on %s, expected %s; not integrating into an unexpected branch",
			worktree, branch, expectedBranch)
	}

	// Never integrate over uncommitted work.
	clean, err := i.IsClean(worktree)
	if err != nil {
		return result, err
	}
	if !clean {
		return result, fmt.Errorf(
			"worktree %s has uncommitted changes; commit or stash them before integrating",
			worktree)
	}

	if err := i.exists(worktree, commit); err != nil {
		return result, err
	}

	// Patch-equivalence, not just ancestry: a previous cherry-pick rewrote the
	// SHA, and applying the same change twice would conflict or duplicate it.
	already, err := i.AlreadyApplied(worktree, commit)
	if err != nil {
		return result, err
	}
	if already {
		head, err := i.Head(worktree)
		if err != nil {
			return result, err
		}
		result.Method = MethodNone
		result.LocalCommit = head
		result.Already = true
		return result, nil
	}

	canFF, err := i.CanFastForward(worktree, commit)
	if err != nil {
		return result, err
	}

	if canFF {
		if err := i.FastForward(worktree, commit); err != nil {
			return result, err
		}
		result.Method = MethodFastForward
		result.LocalCommit = commit // identical by definition
		return result, nil
	}

	local, err := i.CherryPick(worktree, commit)
	if err != nil {
		return result, err
	}

	result.Method = MethodCherryPick
	result.LocalCommit = local

	return result, nil
}

// short abbreviates a SHA for messages.
func short(sha string) string {
	if len(sha) <= 10 {
		return sha
	}
	return sha[:10]
}
