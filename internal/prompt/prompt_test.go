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
	write(RolePath("coder"), "coder rules here")

	return root
}

func TestLoadForRole(t *testing.T) {
	root := fixture(t)

	set, err := LoadForRole(root, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if set.Constitution != "shared rules here" || set.Instructions != "coder rules here" {
		t.Errorf("unexpected set: %+v", set)
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

func TestLoadForRoleRejectsUnsafeNames(t *testing.T) {
	root := fixture(t)

	for _, role := range []string{"", ".", "..", "../escape", "a/b"} {
		if _, err := LoadForRole(root, role); err == nil {
			t.Errorf("LoadForRole accepted unsafe role %q", role)
		}
	}
}

func TestAssemble(t *testing.T) {
	set := PromptSet{Role: "coder", Constitution: "CONSTITUTION-BODY", Instructions: "ROLE-BODY"}

	got := Assemble(set, RuntimeContext{
		Role:        "coder",
		RepoRoot:    "/repo",
		Worktree:    "/repo/.swarm/worktrees/wt-coder",
		Branch:      "swarm/coder",
		ReceiveMode: "task",
	})

	for _, want := range []string{
		"CONSTITUTION-BODY",
		"ROLE-BODY",
		"# Role: coder",
		"- branch: swarm/coder",
		"- worktree: /repo/.swarm/worktrees/wt-coder",
		"- receive mode: task",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("assembled prompt is missing %q\n---\n%s", want, got)
		}
	}

	// The constitution must come before the role instructions.
	if strings.Index(got, "CONSTITUTION-BODY") > strings.Index(got, "ROLE-BODY") {
		t.Error("role instructions precede the constitution")
	}
}

func TestAssembleOmitsEmptyContext(t *testing.T) {
	got := Assemble(PromptSet{Role: "coder", Constitution: "c", Instructions: "r"}, RuntimeContext{Role: "coder"})

	if strings.Contains(got, "- branch:") {
		t.Errorf("empty context field was rendered:\n%s", got)
	}
}
