package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	b, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "codex" || b.Executable() != "codex" {
		t.Errorf("unexpected backend: %+v", b)
	}
}

func TestLookupUnknownBackend(t *testing.T) {
	_, err := Lookup("foo")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `unsupported backend "foo"`) {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestCodexCommand(t *testing.T) {
	got := Codex{}.Command("/repo/.swarm/runtime/prompts/coder.prompt", "/repo/.swarm/worktrees/wt-coder")

	want := `codex --cd '/repo/.swarm/worktrees/wt-coder' "$(cat '/repo/.swarm/runtime/prompts/coder.prompt')"`
	if got != want {
		t.Errorf("Command =\n%s\nwant\n%s", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/plain/path":    `'/plain/path'`,
		"with space":     `'with space'`,
		"it's":           `'it'\''s'`,
		"; rm -rf /":     `'; rm -rf /'`,
		"$(touch pwned)": `'$(touch pwned)'`,
		"`touch pwned`":  "'`touch pwned`'",
	}

	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRuntimePromptPath(t *testing.T) {
	m := &Manager{RepoRoot: "/repo"}

	want := filepath.Join("/repo", ".swarm", "runtime", "prompts", "coder.prompt")
	if got := m.RuntimePromptPath("coder"); got != want {
		t.Errorf("RuntimePromptPath = %q, want %q", got, want)
	}

	// Generated prompts must live under .swarm/, which is gitignored.
	if !strings.HasPrefix(RuntimePromptDir, ".swarm/") {
		t.Errorf("RuntimePromptDir = %q, want it under .swarm/", RuntimePromptDir)
	}
}

func TestWritePrompt(t *testing.T) {
	m := &Manager{RepoRoot: t.TempDir()}

	path, err := m.WritePrompt("coder", "ASSEMBLED")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ASSEMBLED" {
		t.Errorf("prompt file contains %q", data)
	}

	// Overwriting an existing prompt must succeed.
	if _, err := m.WritePrompt("coder", "AGAIN"); err != nil {
		t.Fatalf("second WritePrompt: %v", err)
	}
}

func TestShellsAreNotAgents(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish", "sh"} {
		if !shells[sh] {
			t.Errorf("%q should be recognised as a shell", sh)
		}
	}
	if shells["node"] || shells["codex"] {
		t.Error("an agent process was classified as a shell")
	}
}
