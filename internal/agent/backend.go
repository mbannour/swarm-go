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
	return fmt.Sprintf(
		"codex --cd %s \"$(cat %s)\"",
		shellQuote(workdir),
		shellQuote(promptPath),
	)
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
	return fmt.Sprintf(
		"%s %s %s %s",
		FakeAgentExecutable,
		shellQuote(role),
		shellQuote(workdir),
		shellQuote(promptPath),
	)
}

// shellQuote wraps s in single quotes so the shell treats it as one literal
// word, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
