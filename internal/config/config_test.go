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
	want := RoleConfig{
		Name: "coder", Backend: "codex", Worktree: "wt-coder",
		ReceiveMode: ReceiveTask, Approval: ApprovalInteractive,
	}
	if cfg.Roles[1] != want {
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

// Approval is optional and defaults to interactive, so existing four-field
// configurations keep working and autonomy is never granted implicitly.
func TestApprovalPolicyDefaultsToInteractive(t *testing.T) {
	cfg, err := Load(write(t, fourPack))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range cfg.Roles {
		if r.Approval != ApprovalInteractive {
			t.Errorf("%s approval = %q, want interactive by default", r.Name, r.Approval)
		}
	}
}

func TestApprovalPolicyIsParsed(t *testing.T) {
	body := `window specifier codex wt-specifier task autonomous
window coder codex wt-coder task restricted
window refactorer codex wt-refactorer task interactive
window architect codex wt-architect batch autonomous
`

	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]ApprovalPolicy{
		"specifier":  ApprovalAutonomous,
		"coder":      ApprovalRestricted,
		"refactorer": ApprovalInteractive,
		"architect":  ApprovalAutonomous,
	}

	for _, r := range cfg.Roles {
		if r.Approval != want[r.Name] {
			t.Errorf("%s approval = %q, want %q", r.Name, r.Approval, want[r.Name])
		}
	}
}

func TestUnknownApprovalPolicyIsRejected(t *testing.T) {
	if _, err := Load(write(t, "window coder codex wt-coder task yolo\n")); err == nil {
		t.Error("an unknown approval policy was accepted")
	}
}
