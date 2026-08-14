package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// Regression C — an autonomous role must launch with the backend's validated
// unattended configuration.
//
// The real run stalled because Codex was started with no approval policy and
// stopped to ask permission before `git commit`. An autonomous launch must
// carry the flags that prevent that.
func TestRegressionC_AutonomousLaunchCarriesApprovalFlags(t *testing.T) {
	codex := Codex{}
	line, err := codex.Launch("coder", "/repo/.swarm/runtime/prompts/coder.prompt",
		"/repo/.swarm/worktrees/wt-coder", ApprovalAutonomous)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--ask-for-approval never",
		"--sandbox workspace-write",
		"--cd '/repo/.swarm/worktrees/wt-coder'",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("autonomous launch is missing %q:\n%s", want, line)
		}
	}
}

// Interactive stays interactive: autonomy is opted into, never assumed.
func TestInteractiveLaunchAsksForApproval(t *testing.T) {
	codex := Codex{}
	line, err := codex.Launch("coder", "/p", "/w", ApprovalInteractive)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(line, "--ask-for-approval") || strings.Contains(line, "--sandbox") {
		t.Errorf("interactive launch silently granted autonomy:\n%s", line)
	}
}

func TestRestrictedLaunchIsReadOnly(t *testing.T) {
	codex := Codex{}
	line, err := codex.Launch("coder", "/p", "/w", ApprovalRestricted)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(line, "--sandbox read-only") {
		t.Errorf("restricted launch is not read-only:\n%s", line)
	}
}

// An unsupported policy must fail loudly rather than quietly downgrading.
func TestUnsupportedPolicyIsAnError(t *testing.T) {
	codex := Codex{}
	if _, err := codex.Launch("coder", "/p", "/w", Approval("yolo")); err == nil {
		t.Fatal("an unknown approval policy was accepted")
	}
}

func TestCapabilities(t *testing.T) {
	codex := Codex{}
	caps := codex.Capabilities()

	if !caps.Interactive {
		t.Error("codex is a terminal UI")
	}
	if !caps.WorkspaceTrust {
		t.Error("codex gates on workspace trust; capabilities must say so")
	}
	if !caps.Supports(ApprovalAutonomous) || !caps.Supports(ApprovalInteractive) {
		t.Errorf("capabilities = %+v", caps)
	}
	if caps.Supports(Approval("nonsense")) {
		t.Error("capabilities claim an unknown policy")
	}
}

// An unattended launch must be refused while the workspace is untrusted:
// four agents waiting at four trust prompts is not a started swarm.
func TestReadinessBlocksOnUntrustedWorkspace(t *testing.T) {
	if !Available(Codex{}) {
		t.Skip("codex not installed")
	}

	// A path that cannot plausibly be in the user's codex config.
	codex := Codex{}
	state, reason := codex.Ready(t.TempDir(), ApprovalAutonomous)

	switch state {
	case ReadinessBlockedTrust:
		if !strings.Contains(reason, "swarm bootstrap") {
			t.Errorf("the reason does not say how to fix it: %q", reason)
		}
	case ReadinessNotAuthed:
		t.Skip("codex is not authenticated on this machine")
	default:
		t.Errorf("readiness = %q (%s), want blocked-trust for an untrusted path", state, reason)
	}
}

// Interactive use may hit the trust prompt: a human is sitting there.
func TestReadinessAllowsInteractiveWithoutTrust(t *testing.T) {
	if !Available(Codex{}) {
		t.Skip("codex not installed")
	}

	codex := Codex{}
	state, _ := codex.Ready(t.TempDir(), ApprovalInteractive)
	if state != ReadinessReady && state != ReadinessNotAuthed {
		t.Errorf("interactive readiness = %q, want ready", state)
	}
}

// A sandboxed agent must be able to reach its toolchain's cache.
//
// This is the blocker the first real smoke run hit: with only the worktree
// writable, `go test` could not write its build cache, so the coder could not
// verify its work and correctly refused to commit unverified code.
func TestAutonomousLaunchGrantsWritableRoots(t *testing.T) {
	codex := Codex{}

	line, err := codex.LaunchWith("coder", "/p", "/w", ApprovalAutonomous,
		[]string{"/home/dev/.cache/go-build", "/home/dev/go/pkg/mod"})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--add-dir '/home/dev/.cache/go-build'",
		"--add-dir '/home/dev/go/pkg/mod'",
		"--sandbox workspace-write",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("launch is missing %q:\n%s", want, line)
		}
	}
}

// Interactive runs have no sandbox, so extra roots are meaningless there and
// must not appear.
func TestInteractiveLaunchIgnoresWritableRoots(t *testing.T) {
	codex := Codex{}

	line, err := codex.LaunchWith("coder", "/p", "/w", ApprovalInteractive,
		[]string{"/home/dev/.cache/go-build"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(line, "--add-dir") {
		t.Errorf("interactive launch carries sandbox flags:\n%s", line)
	}
}

// Paths are quoted: a directory with a space must not split into two flags.
func TestWritableRootsAreShellQuoted(t *testing.T) {
	codex := Codex{}

	line, err := codex.LaunchWith("coder", "/p", "/w", ApprovalAutonomous,
		[]string{"/home/some one/.cache"})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(line, `--add-dir '/home/some one/.cache'`) {
		t.Errorf("writable root was not quoted:\n%s", line)
	}
}

// A role commits from a linked worktree, whose Git metadata lives in the main
// repository — so the sandbox must always include it.
//
// The first real run got as far as "go test passed, go build passed" and then
// could not commit: .git/worktrees/<role> was read-only.
func TestSandboxAlwaysGrantsGitDirectory(t *testing.T) {
	m := &Manager{RepoRoot: "/repo"}

	roots := m.writableRoots(Role{
		Name: "coder", WritableRoots: []string{"/home/dev/.cache/go-build"},
	})

	if len(roots) == 0 || roots[0] != filepath.Join("/repo", ".git") {
		t.Fatalf("roots = %v, want the repository .git first", roots)
	}

	var found bool
	for _, r := range roots {
		if r == "/home/dev/.cache/go-build" {
			found = true
		}
	}
	if !found {
		t.Error("configured writable roots were dropped")
	}
}

// `trusted` is the opt-in escape hatch for unattended commits: Codex's
// workspace-write sandbox protects .git, so a linked worktree cannot be
// committed to under it.
func TestTrustedLaunchDisablesTheSandbox(t *testing.T) {
	codex := Codex{}

	line, err := codex.Launch("coder", "/p", "/w", ApprovalTrusted)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(line, "--sandbox danger-full-access") {
		t.Errorf("trusted launch is still sandboxed:\n%s", line)
	}
	if !strings.Contains(line, "--ask-for-approval never") {
		t.Errorf("trusted launch still asks for approval:\n%s", line)
	}
}

// It must never be reachable by accident: only by naming it.
func TestSandboxIsOnUnlessTrustedIsChosen(t *testing.T) {
	codex := Codex{}

	for _, policy := range []Approval{ApprovalInteractive, ApprovalAutonomous, ApprovalRestricted} {
		line, err := codex.Launch("coder", "/p", "/w", policy)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "danger-full-access") {
			t.Errorf("%s silently disabled the sandbox:\n%s", policy, line)
		}
	}
}
