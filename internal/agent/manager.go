package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mbannour/swarm-go/internal/tmux"
)

// RuntimePromptDir is the repository-relative home of generated prompt files.
// It lives under .swarm/, which is gitignored, so prompts are never committed.
const RuntimePromptDir = ".swarm/runtime/prompts"

// State is what `agents list` reports for a role.
type State string

const (
	StateRunning        State = "running"
	StateNotStarted     State = "not-started"
	StateSessionMissing State = "session-missing"
	StateBackendMissing State = "backend-missing"
)

// shells are the pane commands that mean "no agent is running here". Anything
// else in the foreground is conservatively treated as the agent.
var shells = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true,
	"dash": true, "ksh": true, "tcsh": true, "csh": true,
}

// Role is one configured role, resolved by the caller.
type Role struct {
	Name        string
	Backend     string
	Worktree    string // absolute path
	Branch      string
	ReceiveMode string
}

// Manager launches and stops agents in existing tmux sessions.
type Manager struct {
	RepoRoot string
	Tmux     *tmux.Manager
}

// NewManager returns a manager for a repository and its tmux server.
func NewManager(repoRoot string, t *tmux.Manager) *Manager {
	return &Manager{RepoRoot: repoRoot, Tmux: t}
}

// RuntimePromptPath is the absolute path of a role's generated prompt file.
func (m *Manager) RuntimePromptPath(role string) string {
	return filepath.Join(m.RepoRoot, filepath.FromSlash(RuntimePromptDir), role+".prompt")
}

// WritePrompt stores the assembled prompt for a role and returns its path.
func (m *Manager) WritePrompt(role, assembled string) (string, error) {
	path := m.RuntimePromptPath(role)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(assembled), 0o600); err != nil {
		return "", err
	}

	return path, nil
}

// Status inspects one role without changing anything.
func (m *Manager) Status(r Role) (State, error) {
	backend, err := Lookup(r.Backend)
	if err != nil {
		return StateBackendMissing, nil
	}

	session := tmux.SessionName(r.Name)

	live, err := m.Tmux.HasSession(session)
	if err != nil {
		return "", err
	}
	if !live {
		return StateSessionMissing, nil
	}

	if !Available(backend) {
		return StateBackendMissing, nil
	}

	cmd, err := m.Tmux.PaneCommand(session)
	if err != nil {
		return "", err
	}
	if shells[strings.TrimSpace(cmd)] {
		return StateNotStarted, nil
	}

	return StateRunning, nil
}

// Start launches the role's backend in its existing tmux session, using the
// already-assembled prompt. It is idempotent: if something is already running
// in the pane, it reports started=false and types nothing.
func (m *Manager) Start(r Role, assembled string) (started bool, err error) {
	backend, err := Lookup(r.Backend)
	if err != nil {
		return false, fmt.Errorf("%w for role %q", err, r.Name)
	}

	state, err := m.Status(r)
	if err != nil {
		return false, err
	}

	switch state {
	case StateSessionMissing:
		return false, fmt.Errorf(
			"tmux session %s is not running\n\nrun:\n  swarm sessions create",
			tmux.SessionName(r.Name),
		)
	case StateBackendMissing:
		return false, fmt.Errorf(
			"%s backend is configured for role %q but executable was not found in PATH",
			backend.Name(), r.Name,
		)
	case StateRunning:
		return false, nil
	}

	promptPath, err := m.WritePrompt(r.Name, assembled)
	if err != nil {
		return false, err
	}

	session := tmux.SessionName(r.Name)
	line := backend.Command(promptPath, r.Worktree)

	// The command line and the Enter key are sent as separate tmux arguments;
	// the prompt text itself stays in the file.
	if err := m.Tmux.SendKeys(session, line); err != nil {
		return false, err
	}
	if err := m.Tmux.SendKeys(session, "Enter"); err != nil {
		return false, err
	}

	return true, nil
}

// Stop interrupts the agent with Ctrl-C, leaving the tmux session alive.
func (m *Manager) Stop(r Role) (stopped bool, err error) {
	state, err := m.Status(r)
	if err != nil {
		return false, err
	}
	if state != StateRunning {
		return false, nil
	}

	if err := m.Tmux.SendKeys(tmux.SessionName(r.Name), "C-c"); err != nil {
		return false, err
	}

	return true, nil
}
