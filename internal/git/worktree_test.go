package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchName(t *testing.T) {
	cases := map[string]string{
		"specifier":  "swarm/specifier",
		"coder":      "swarm/coder",
		"refactorer": "swarm/refactorer",
		"architect":  "swarm/architect",
	}

	for role, want := range cases {
		if got := BranchName(role); got != want {
			t.Errorf("BranchName(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestPlanPaths(t *testing.T) {
	m := &WorktreeManager{Root: filepath.Join("/repo")}

	wt, err := m.Plan("coder", "wt-coder")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if wt.RelPath != ".swarm/worktrees/wt-coder" {
		t.Errorf("RelPath = %q", wt.RelPath)
	}
	if want := filepath.Join("/repo", ".swarm", "worktrees", "wt-coder"); wt.AbsPath != want {
		t.Errorf("AbsPath = %q, want %q", wt.AbsPath, want)
	}
	if wt.Branch != "swarm/coder" {
		t.Errorf("Branch = %q", wt.Branch)
	}
}

func TestPlanRejectsUnsafeNames(t *testing.T) {
	m := &WorktreeManager{Root: "/repo"}

	unsafe := []string{"", ".", "..", "../escape", "a/b", `a\b`, "-rf"}

	for _, name := range unsafe {
		if _, err := m.Plan("coder", name); err == nil {
			t.Errorf("Plan accepted unsafe worktree name %q", name)
		}
		if _, err := m.Plan(name, "wt-coder"); err == nil {
			t.Errorf("Plan accepted unsafe role name %q", name)
		}
	}
}

func TestBaseDir(t *testing.T) {
	m := &WorktreeManager{Root: "/repo"}
	if want := filepath.Join("/repo", ".swarm", "worktrees"); m.BaseDir() != want {
		t.Errorf("BaseDir = %q, want %q", m.BaseDir(), want)
	}
}

// TestWorktreeLifecycle exercises create/list/remove against a throwaway repo.
func TestWorktreeLifecycle(t *testing.T) {
	if !Available() {
		t.Skip("git not available")
	}

	dir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "swarm@example.com"},
		{"config", "user.name", "swarm"},
	} {
		if _, err := run(dir, args...); err != nil {
			t.Skipf("git unusable: %v", err)
		}
	}

	m := &WorktreeManager{Root: dir}
	refs := []RoleRef{{Name: "coder", Worktree: "wt-coder"}}

	// No commit yet: creating must fail with a clear, typed error.
	if _, _, err := m.Create("coder", "wt-coder"); err != ErrNoCommits {
		t.Fatalf("Create before first commit = %v, want ErrNoCommits", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(dir, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(dir, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}

	wt, created, err := m.Create("coder", "wt-coder")
	if err != nil || !created {
		t.Fatalf("Create = (%v, %v)", created, err)
	}
	if _, err := os.Stat(wt.AbsPath); err != nil {
		t.Fatalf("worktree directory missing: %v", err)
	}
	if !branchExists(dir, "swarm/coder") {
		t.Error("branch swarm/coder was not created")
	}

	// Idempotent second run.
	if _, created, err := m.Create("coder", "wt-coder"); err != nil || created {
		t.Fatalf("second Create = (%v, %v), want (false, nil)", created, err)
	}

	statuses, err := m.List(refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || !statuses[0].Present {
		t.Fatalf("List = %+v, want one present worktree", statuses)
	}

	if _, removed, err := m.Remove("coder", "wt-coder", false); err != nil || !removed {
		t.Fatalf("Remove = (%v, %v)", removed, err)
	}
	if _, err := os.Stat(wt.AbsPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still present: %v", err)
	}

	// Removing again is a no-op, not an error.
	if _, removed, err := m.Remove("coder", "wt-coder", false); err != nil || removed {
		t.Fatalf("second Remove = (%v, %v), want (false, nil)", removed, err)
	}

	statuses, err = m.List(refs)
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Present {
		t.Error("List still reports the worktree as present")
	}
}
