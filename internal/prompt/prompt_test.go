package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptPaths(t *testing.T) {
	if got := ConstitutionPath(); got != "prompts/constitution.prompt" {
		t.Errorf("ConstitutionPath = %q", got)
	}
	if got := RuntimePath(); got != "prompts/runtime.prompt" {
		t.Errorf("RuntimePath = %q", got)
	}

	cases := map[string]string{
		"specifier":  "prompts/roles/specifier.prompt",
		"coder":      "prompts/roles/coder.prompt",
		"refactorer": "prompts/roles/refactorer.prompt",
		"architect":  "prompts/roles/architect.prompt",
	}

	for role, want := range cases {
		if got := RolePath(role); got != want {
			t.Errorf("RolePath(%q) = %q, want %q", role, got, want)
		}
	}
}

// fixture builds a prompts/ tree in a temp dir and returns its root.
func fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prompts", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(ConstitutionPath(), "shared rules here")
	write(RuntimePath(), "runtime protocol here")
	write(RolePath("coder"), "coder rules here")

	return root
}

func TestLoadForRole(t *testing.T) {
	root := fixture(t)

	set, err := LoadForRole(root, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if set.Constitution != "shared rules here" {
		t.Errorf("Constitution = %q", set.Constitution)
	}
	if set.Runtime != "runtime protocol here" {
		t.Errorf("Runtime = %q", set.Runtime)
	}
	if set.Instructions != "coder rules here" {
		t.Errorf("Instructions = %q", set.Instructions)
	}
	if set.Role != "coder" {
		t.Errorf("Role = %q", set.Role)
	}
}

func TestLoadForRoleMissingFile(t *testing.T) {
	root := fixture(t)

	_, err := LoadForRole(root, "architect")
	if err == nil {
		t.Fatal("expected an error for a missing role prompt")
	}
	if !strings.Contains(err.Error(), "prompts/roles/architect.prompt does not exist") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestLoadForRoleMissingRuntimePrompt(t *testing.T) {
	root := fixture(t)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(RuntimePath()))); err != nil {
		t.Fatal(err)
	}

	_, err := LoadForRole(root, "coder")
	if err == nil {
		t.Fatal("expected an error when the runtime prompt is missing")
	}
	if !strings.Contains(err.Error(), "prompts/runtime.prompt does not exist") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestLoadForRoleRejectsUnsafeNames(t *testing.T) {
	root := fixture(t)

	for _, role := range []string{"", ".", "..", "../escape", "a/b"} {
		if _, err := LoadForRole(root, role); err == nil {
			t.Errorf("LoadForRole accepted unsafe role %q", role)
		}
	}
}

func testContext() RuntimeContext {
	return RuntimeContext{
		Role:        "coder",
		RepoRoot:    "/repo",
		Worktree:    "/repo/.swarm/worktrees/wt-coder",
		Branch:      "swarm/coder",
		ReceiveMode: "task",
		NextRole:    "refactorer",
		SwarmBin:    "/repo/bin/swarm",
	}
}

func testSet() PromptSet {
	return PromptSet{
		Role:         "coder",
		Constitution: "CONSTITUTION-BODY",
		Runtime:      "RUNTIME-BODY",
		Instructions: "ROLE-BODY",
	}
}

func TestAssembleIncludesEverySection(t *testing.T) {
	got := Assemble(testSet(), testContext())

	for _, want := range []string{
		"CONSTITUTION-BODY",
		"RUNTIME-BODY",
		"ROLE-BODY",
		"# Swarm constitution",
		"# Swarm runtime protocol",
		"# Role: coder",
		"# Runtime context",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("assembled prompt is missing %q", want)
		}
	}
}

func TestAssembleSectionOrder(t *testing.T) {
	got := Assemble(testSet(), testContext())

	order := []string{"CONSTITUTION-BODY", "RUNTIME-BODY", "ROLE-BODY", "# Runtime context"}

	pos := -1
	for _, marker := range order {
		i := strings.Index(got, marker)
		if i < 0 {
			t.Fatalf("missing %q", marker)
		}
		if i < pos {
			t.Errorf("%q appears out of order", marker)
		}
		pos = i
	}
}

func TestAssembleRuntimeContextValues(t *testing.T) {
	got := Assemble(testSet(), testContext())

	for _, want := range []string{
		"ROLE=coder",
		"REPOSITORY_ROOT=/repo",
		"WORKTREE=/repo/.swarm/worktrees/wt-coder",
		"BRANCH=swarm/coder",
		"RECEIVE_MODE=task",
		"NEXT_ROLE=refactorer",
		"SWARM_BIN=/repo/bin/swarm",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runtime context is missing %q", want)
		}
	}
}

// The lifecycle commands must be spelled with the resolved binary, never a
// bare `swarm` that would not exist inside a worktree.
func TestAssembleLifecycleCommandsUseTheBinary(t *testing.T) {
	got := Assemble(testSet(), testContext())

	for _, want := range []string{
		`SWARM_BIN='/repo/bin/swarm'`,
		`"$SWARM_BIN" handoff ready coder`,
		`"$SWARM_BIN" handoff current coder`,
		`"$SWARM_BIN" handoff status coder`,
		`"$SWARM_BIN" handoff next --from coder`,
		`"$SWARM_BIN" handoff done coder`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("assembled prompt is missing command %q", want)
		}
	}

	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "swarm handoff") {
			t.Errorf("prompt tells the agent to run a bare binary: %q", trimmed)
		}
	}
}

func TestAssembleQuotesAwkwardBinaryPaths(t *testing.T) {
	ctx := testContext()
	ctx.SwarmBin = "/home/some one/repo/bin/swarm"

	got := Assemble(testSet(), ctx)

	if !strings.Contains(got, `SWARM_BIN='/home/some one/repo/bin/swarm'`) {
		t.Errorf("binary path was not shell-quoted:\n%s", got)
	}
}

func TestAssembleMentionsBatchModeOnlyWhenRelevant(t *testing.T) {
	if got := Assemble(testSet(), testContext()); strings.Contains(got, "receive mode is `batch`") {
		t.Error("task-mode prompt describes batch behavior")
	}

	ctx := testContext()
	ctx.ReceiveMode = "batch"
	if got := Assemble(testSet(), ctx); !strings.Contains(got, "receive mode is `batch`") {
		t.Error("batch-mode prompt does not describe batch behavior")
	}
}

func TestAssembleOmitsEmptyContext(t *testing.T) {
	got := Assemble(testSet(), RuntimeContext{Role: "coder"})

	if strings.Contains(got, "BRANCH=") || strings.Contains(got, "SWARM_BIN=") {
		t.Errorf("empty context fields were rendered:\n%s", got)
	}
}

// The shipped prompts must satisfy the same rules the tests assert on
// fixtures: this catches a real prompt file drifting out of shape.
func TestShippedPromptsAssemble(t *testing.T) {
	root := repoRoot(t)

	for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
		set, err := LoadForRole(root, role)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}

		got := Assemble(set, RuntimeContext{
			Role:        role,
			RepoRoot:    root,
			Worktree:    filepath.Join(root, ".swarm", "worktrees", "wt-"+role),
			Branch:      "swarm/" + role,
			ReceiveMode: "task",
			NextRole:    "coder",
			SwarmBin:    filepath.Join(root, "bin", "swarm"),
		})

		for _, want := range []string{
			"handoff ready",
			"handoff done",
			"NO_TASK",
			"rev-parse --short=10",
			"# Runtime context",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s prompt lacks %q", role, want)
			}
		}
	}
}

// repoRoot walks up to the module root so tests find prompts/ wherever they run.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	t.Skip("module root not found")
	return ""
}
