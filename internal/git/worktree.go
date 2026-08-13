package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorktreesDir is the repository-relative home of every managed worktree.
const WorktreesDir = ".swarm/worktrees"

// BranchPrefix is prepended to a role name to form its branch.
const BranchPrefix = "swarm/"

// ErrNoCommits is returned when the repository has no commit to branch from.
var ErrNoCommits = errors.New("repository has no commits yet")

// Worktree is the managed worktree belonging to one role.
type Worktree struct {
	Role    string
	Branch  string
	RelPath string // repository-relative, e.g. .swarm/worktrees/wt-coder
	AbsPath string
}

// BranchName maps a role name to its dedicated branch name.
func BranchName(role string) string {
	return BranchPrefix + role
}

// validName rejects anything that could escape .swarm/worktrees.
func validName(kind, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("empty %s", kind)
	case name == "." || name == "..":
		return fmt.Errorf("invalid %s %q", kind, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s %q must not contain a path separator", kind, name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("%s %q must not start with a dash", kind, name)
	}
	return nil
}

// WorktreeManager creates, lists and removes the worktrees of a repository.
type WorktreeManager struct {
	Root string // absolute path to the repository top level
}

// NewManager locates the repository containing dir and returns its manager.
func NewManager(dir string) (*WorktreeManager, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return nil, err
	}
	return &WorktreeManager{Root: root}, nil
}

// BaseDir is the absolute path of .swarm/worktrees.
func (m *WorktreeManager) BaseDir() string {
	return filepath.Join(m.Root, filepath.FromSlash(WorktreesDir))
}

// Plan resolves the worktree a role/worktree-name pair maps to, rejecting any
// name that would place it outside .swarm/worktrees.
func (m *WorktreeManager) Plan(role, worktree string) (Worktree, error) {
	if err := validName("role name", role); err != nil {
		return Worktree{}, err
	}
	if err := validName("worktree name", worktree); err != nil {
		return Worktree{}, err
	}

	rel := WorktreesDir + "/" + worktree

	return Worktree{
		Role:    role,
		Branch:  BranchName(role),
		RelPath: rel,
		AbsPath: filepath.Join(m.Root, filepath.FromSlash(rel)),
	}, nil
}

// Exists reports whether git currently tracks wt as a worktree.
func (m *WorktreeManager) Exists(wt Worktree) (bool, error) {
	paths, err := listWorktreePaths(m.Root)
	if err != nil {
		return false, err
	}

	want := resolve(wt.AbsPath)
	for _, p := range paths {
		if resolve(p) == want {
			return true, nil
		}
	}

	return false, nil
}

// Create adds the worktree for a role on its own branch.
//
// It is idempotent: if the worktree is already registered at the expected path
// it reports created=false and leaves everything untouched.
func (m *WorktreeManager) Create(role, worktree string) (wt Worktree, created bool, err error) {
	wt, err = m.Plan(role, worktree)
	if err != nil {
		return Worktree{}, false, err
	}

	if !HasCommits(m.Root) {
		return wt, false, ErrNoCommits
	}

	exists, err := m.Exists(wt)
	if err != nil {
		return wt, false, err
	}
	if exists {
		return wt, false, nil
	}

	// A leftover directory that git does not know about would make `git
	// worktree add` fail with a confusing message.
	if _, statErr := os.Stat(wt.AbsPath); statErr == nil {
		return wt, false, fmt.Errorf("%s already exists but is not a registered worktree; move it aside", wt.RelPath)
	}

	if err := os.MkdirAll(m.BaseDir(), 0o755); err != nil {
		return wt, false, err
	}

	// Reuse the branch if a previous run left it behind, otherwise create it.
	args := []string{"worktree", "add"}
	if branchExists(m.Root, wt.Branch) {
		args = append(args, wt.AbsPath, wt.Branch)
	} else {
		args = append(args, "-b", wt.Branch, wt.AbsPath)
	}

	if _, err := run(m.Root, args...); err != nil {
		return wt, false, err
	}

	return wt, true, nil
}

// Status is one row of List: the configured worktree plus whether it is present.
type Status struct {
	Worktree
	Present bool
}

// List reports the state of every configured role's worktree.
func (m *WorktreeManager) List(roles []RoleRef) ([]Status, error) {
	paths, err := listWorktreePaths(m.Root)
	if err != nil {
		return nil, err
	}

	present := make(map[string]bool, len(paths))
	for _, p := range paths {
		present[resolve(p)] = true
	}

	out := make([]Status, 0, len(roles))
	for _, r := range roles {
		wt, err := m.Plan(r.Name, r.Worktree)
		if err != nil {
			return nil, fmt.Errorf("role %s: %w", r.Name, err)
		}
		out = append(out, Status{Worktree: wt, Present: present[resolve(wt.AbsPath)]})
	}

	return out, nil
}

// Remove removes a managed worktree via `git worktree remove`.
//
// It never deletes a directory itself, and refuses anything that is not
// registered with git at the expected .swarm/worktrees path. removed=false
// means there was nothing to do.
func (m *WorktreeManager) Remove(role, worktree string, force bool) (wt Worktree, removed bool, err error) {
	wt, err = m.Plan(role, worktree)
	if err != nil {
		return Worktree{}, false, err
	}

	exists, err := m.Exists(wt)
	if err != nil {
		return wt, false, err
	}
	if !exists {
		return wt, false, nil
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.AbsPath)

	if _, err := run(m.Root, args...); err != nil {
		return wt, false, err
	}

	return wt, true, nil
}

// RoleRef is the subset of a configured role the manager needs. It keeps this
// package independent of the config package.
type RoleRef struct {
	Name     string
	Worktree string
}

// resolve makes paths comparable across symlinked parents (e.g. /tmp on macOS).
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}
