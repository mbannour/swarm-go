package agent

import (
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
