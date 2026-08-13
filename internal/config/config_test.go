package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swarm.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const fourPack = `# comment

window specifier codex wt-specifier task
window coder codex wt-coder task
window refactorer codex wt-refactorer task
window architect codex wt-architect task
`

func TestLoadFourPack(t *testing.T) {
	cfg, err := Load(write(t, fourPack))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Roles) != 4 {
		t.Fatalf("got %d roles, want 4", len(cfg.Roles))
	}
	if cfg.Roles[1] != (RoleConfig{Name: "coder", Backend: "codex", Worktree: "wt-coder", ReceiveMode: ReceiveTask}) {
		t.Errorf("unexpected role: %+v", cfg.Roles[1])
	}
	if err := cfg.ValidateFourPack(); err != nil {
		t.Errorf("ValidateFourPack: %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"unknown directive": "pane coder codex wt-coder task\n",
		"wrong field count": "window coder codex wt-coder\n",
		"unknown recv mode": "window coder codex wt-coder stream\n",
	}

	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateFourPackRejects(t *testing.T) {
	cases := map[string]string{
		"too few":   "window coder codex wt-coder task\n",
		"duplicate": strings.Replace(fourPack, "window architect", "window coder", 1),
	}

	for name, body := range cases {
		cfg, err := Load(write(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := cfg.ValidateFourPack(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
