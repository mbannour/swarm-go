// Package agent launches and stops coding-agent processes inside tmux sessions
// that already exist. It does not create sessions or worktrees.
package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

// Backend knows how one provider's CLI is invoked. Adding a provider means
// adding one implementation here and one entry in Lookup.
type Backend interface {
	// Name is the identifier used in swarm.conf.
	Name() string

	// Executable is the binary that must be present in PATH.
	Executable() string

	// Command returns the shell command line to type into the role's pane.
	// promptPath is a file containing the assembled prompt; workdir is the
	// role's worktree. Implementations must quote every interpolated value.
	Command(role, promptPath, workdir string) string

	// Capabilities describes what this backend can be asked to do, so the
	// orchestrator can check "can this run unattended?" instead of assuming.
	Capabilities() Capabilities

	// Launch returns the command line for a role under an approval policy.
	// An unsupported policy must be an error, never a silent downgrade.
	Launch(role, promptPath, workdir string, policy Approval) (string, error)

	// LaunchWith is Launch plus directories the agent may write to outside its
	// worktree — a sandboxed backend otherwise blocks every toolchain whose
	// cache lives in $HOME.
	LaunchWith(role, promptPath, workdir string, policy Approval, writable []string) (string, error)
}

// Approval mirrors config.ApprovalPolicy without importing it, keeping the
// backend layer independent of configuration parsing.
type Approval string

const (
	ApprovalInteractive Approval = "interactive"
	ApprovalAutonomous  Approval = "autonomous"
	ApprovalRestricted  Approval = "restricted"
	// ApprovalTrusted disables the backend's sandbox. Required for unattended
	// commits with Codex, whose workspace-write sandbox protects .git.
	ApprovalTrusted Approval = "trusted"
)

// Capabilities describes a backend's runtime abilities.
type Capabilities struct {
	// Interactive reports whether the backend runs as a terminal UI.
	Interactive bool
	// ApprovalPolicies lists the policies this backend can honour.
	ApprovalPolicies []Approval
	// WorkspaceTrust reports whether the backend gates on trusting a directory
	// before it will run there.
	WorkspaceTrust bool
	// UnattendedStartup reports whether it can reach a working state with no
	// human at the terminal, once bootstrapped.
	UnattendedStartup bool
}

// Supports reports whether a policy is available.
func (c Capabilities) Supports(policy Approval) bool {
	for _, p := range c.ApprovalPolicies {
		if p == policy {
			return true
		}
	}
	return false
}

// Lookup resolves a backend name from swarm.conf.
func Lookup(name string) (Backend, error) {
	switch name {
	case "codex":
		return Codex{}, nil
	case "fake":
		return Fake{}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", name)
	}
}

// Available reports whether the backend's executable is in PATH.
func Available(b Backend) bool {
	_, err := exec.LookPath(b.Executable())
	return err == nil
}

// Codex drives the Codex CLI's interactive TUI.
//
// `codex [OPTIONS] [PROMPT]` starts an interactive session with an optional
// initial prompt, and `-C/--cd` sets the agent's working root. The prompt is
// passed via `"$(cat <file>)"` so that the prompt text itself never travels
// through tmux send-keys — only the fixed, quoted file path does.
type Codex struct{}

func (Codex) Name() string       { return "codex" }
func (Codex) Executable() string { return "codex" }

func (Codex) Command(role, promptPath, workdir string) string {
	line, _ := Codex{}.Launch(role, promptPath, workdir, ApprovalInteractive)
	return line
}

// Capabilities for the Codex CLI, verified against `codex --help`.
func (Codex) Capabilities() Capabilities {
	return Capabilities{
		Interactive: true,
		ApprovalPolicies: []Approval{
			ApprovalInteractive, ApprovalAutonomous, ApprovalRestricted, ApprovalTrusted,
		},
		// Codex asks to trust a directory the first time it runs there, so a
		// worktree must be trusted before an unattended launch.
		WorkspaceTrust:    true,
		UnattendedStartup: true,
	}
}

// Launch builds the Codex command line for an approval policy.
//
// The flags come from `codex --help` on the installed version:
//
//	-a, --ask-for-approval <untrusted|on-request|never>
//	-s, --sandbox <read-only|workspace-write|danger-full-access>
//
//	interactive  no flags — Codex asks before running commands
//	autonomous   -a never -s workspace-write — no prompts, writes confined to
//	             the worktree, network and the wider filesystem still sandboxed
//	restricted   -a never -s read-only — unattended but unable to modify files
func (c Codex) Launch(role, promptPath, workdir string, policy Approval) (string, error) {
	return c.LaunchWith(role, promptPath, workdir, policy, nil)
}

// LaunchWith adds writable roots via `--add-dir`, which `codex --help`
// documents as "additional directories that should be writable alongside the
// primary workspace". Sandboxed policies need it: with only the worktree
// writable, `go test` cannot reach its build cache and quietly fails, so an
// agent told to verify before committing correctly refuses to commit.
func (Codex) LaunchWith(role, promptPath, workdir string, policy Approval, writable []string) (string, error) {
	var flags string

	switch policy {
	case ApprovalInteractive, "":
		flags = ""
	case ApprovalAutonomous:
		flags = " --ask-for-approval never --sandbox workspace-write"
	case ApprovalRestricted:
		flags = " --ask-for-approval never --sandbox read-only"
	case ApprovalTrusted:
		// No sandbox. Codex's workspace-write sandbox keeps .git read-only, and
		// a linked worktree cannot be committed to without it — so an
		// unattended coder needs this, and the operator opts in by name.
		flags = " --ask-for-approval never --sandbox danger-full-access"
	default:
		return "", fmt.Errorf("codex does not support approval policy %q", policy)
	}

	// Extra writable roots only mean anything when a sandbox is in force.
	if policy == ApprovalAutonomous || policy == ApprovalRestricted {
		for _, dir := range writable {
			if dir == "" {
				continue
			}
			flags += " --add-dir " + shellQuote(dir)
		}
	}

	return fmt.Sprintf(
		"codex --cd %s%s \"$(cat %s)\"",
		shellQuote(workdir), flags, shellQuote(promptPath),
	), nil
}

// Fake is a deterministic stand-in used by the acceptance tests. It drives the
// same runtime protocol as a real agent — ready, work, commit, next, done —
// through the same tmux and handoff paths, so the orchestrator is exercised
// without any AI service.
//
// The executable is expected on PATH; the acceptance harness supplies it.
type Fake struct{}

func (Fake) Name() string       { return "fake" }
func (Fake) Executable() string { return FakeAgentExecutable }

// FakeAgentExecutable is the program a `fake` backend launches.
const FakeAgentExecutable = "swarm-fake-agent"

func (Fake) Command(role, promptPath, workdir string) string {
	line, _ := Fake{}.Launch(role, promptPath, workdir, ApprovalAutonomous)
	return line
}

// Capabilities: the fake agent needs no approval and no trust gate.
func (Fake) Capabilities() Capabilities {
	return Capabilities{
		Interactive: false,
		ApprovalPolicies: []Approval{
			ApprovalInteractive, ApprovalAutonomous, ApprovalRestricted, ApprovalTrusted,
		},
		WorkspaceTrust:    false,
		UnattendedStartup: true,
	}
}

func (f Fake) Launch(role, promptPath, workdir string, policy Approval) (string, error) {
	return f.LaunchWith(role, promptPath, workdir, policy, nil)
}

func (Fake) LaunchWith(role, promptPath, workdir string, policy Approval, writable []string) (string, error) {
	return fmt.Sprintf(
		"%s %s %s %s",
		FakeAgentExecutable,
		shellQuote(role),
		shellQuote(workdir),
		shellQuote(promptPath),
	), nil
}

// shellQuote wraps s in single quotes so the shell treats it as one literal
// word, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
