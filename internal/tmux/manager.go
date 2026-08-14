package tmux

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SessionPrefix is prepended to a role name to form its tmux session name.
const SessionPrefix = "swarm-"

// projectIDLen is how many hex characters of the repository-root digest are
// used to identify a project. 12 chars is plenty to keep repositories apart.
const projectIDLen = 12

// Session is the tmux session belonging to one role.
type Session struct {
	Role       string
	Name       string
	WorkingDir string // absolute path to the role's worktree
}

// RoleRef is the subset of a configured role this package needs, so that it
// stays independent of the config package.
type RoleRef struct {
	Name       string
	WorkingDir string
}

// SessionName maps a role name to its deterministic tmux session name.
func SessionName(role string) string {
	return SessionPrefix + role
}

// ProjectID is a deterministic short identifier for a repository root.
// The same absolute path always yields the same id; different paths do not.
func ProjectID(repoRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return hex.EncodeToString(sum[:])[:projectIDLen]
}

// SocketPath returns the project-local tmux socket for a repository root.
//
// It lives under the OS temp directory rather than inside the repository or a
// worktree, so that git operations never see it:
//
//	<tmpdir>/swarm-go-<user>/<project-id>.sock
func SocketPath(repoRoot string) string {
	dir := filepath.Join(os.TempDir(), "swarm-go-"+socketUser())
	return filepath.Join(dir, ProjectID(repoRoot)+".sock")
}

// Manager drives one project's tmux server over its own isolated socket.
type Manager struct {
	Socket string
}

// NewManager returns a manager bound to the socket derived from repoRoot.
func NewManager(repoRoot string) *Manager {
	return &Manager{Socket: SocketPath(repoRoot)}
}

// running returns the names of the sessions on this socket.
//
// An unreachable server (no sessions ever created, or already exited) is not an
// error: it simply means nothing is running.
func (m *Manager) running() (map[string]bool, error) {
	if !Available() {
		return nil, ErrNotInstalled
	}

	out, err := run(m.Socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServer(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}

	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names[line] = true
		}
	}

	return names, nil
}

// isNoServer reports whether err just means "no tmux server on this socket".
func isNoServer(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "error connecting to")
}

// session builds the Session for a role.
func session(r RoleRef) Session {
	return Session{Role: r.Name, Name: SessionName(r.Name), WorkingDir: r.WorkingDir}
}

// Create starts a detached session for the role in its worktree.
//
// It is idempotent: an already-running session reports created=false. The
// worktree must already exist — this package never creates one.
func (m *Manager) Create(r RoleRef) (s Session, created bool, err error) {
	s = session(r)

	if !Available() {
		return s, false, ErrNotInstalled
	}

	info, statErr := os.Stat(s.WorkingDir)
	if statErr != nil || !info.IsDir() {
		return s, false, fmt.Errorf(
			"worktree %s does not exist\n\nrun:\n  swarm worktrees create",
			s.WorkingDir,
		)
	}

	live, err := m.running()
	if err != nil {
		return s, false, err
	}
	if live[s.Name] {
		return s, false, nil
	}

	if err := ensureSocketDir(m.Socket); err != nil {
		return s, false, err
	}

	if _, err := run(m.Socket, "new-session", "-d", "-s", s.Name, "-c", s.WorkingDir); err != nil {
		return s, false, err
	}

	return s, true, nil
}

// HasSession reports whether a session of that exact name is running.
func (m *Manager) HasSession(name string) (bool, error) {
	live, err := m.running()
	if err != nil {
		return false, err
	}
	return live[name], nil
}

// SendKeys sends literal keys to a session's active pane. Each argument is
// passed to tmux as its own argument, never concatenated into a shell string.
func (m *Manager) SendKeys(name string, keys ...string) error {
	args := append([]string{"send-keys", "-t", name}, keys...)
	_, err := run(m.Socket, args...)
	return err
}

// PaneCommand returns the command name of the process in the foreground of a
// session's active pane (tmux's #{pane_current_command}).
func (m *Manager) PaneCommand(name string) (string, error) {
	return run(m.Socket, "display-message", "-p", "-t", name, "#{pane_current_command}")
}

// Status is one row of List: the role's session plus whether it is running.
type Status struct {
	Session
	Running bool
}

// List reports the state of every configured role's session. A tmux server
// that is not running yields all-missing rather than an error.
func (m *Manager) List(roles []RoleRef) ([]Status, error) {
	live, err := m.running()
	if err != nil {
		return nil, err
	}

	out := make([]Status, 0, len(roles))
	for _, r := range roles {
		s := session(r)
		out = append(out, Status{Session: s, Running: live[s.Name]})
	}

	return out, nil
}

// Attach replaces the current terminal's I/O with the role's session.
// It blocks until the user detaches.
func (m *Manager) Attach(r RoleRef) error {
	s := session(r)

	if !Available() {
		return ErrNotInstalled
	}

	live, err := m.running()
	if err != nil {
		return err
	}
	if !live[s.Name] {
		return fmt.Errorf("session for role %q is not running", r.Name)
	}

	cmd := exec.Command("tmux", "-S", m.Socket, "attach-session", "-t", s.Name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("attach to %s: %w", s.Name, err)
	}

	return nil
}

// Remove kills the role's session. A session that is not running is treated as
// already removed (removed=false), not an error.
func (m *Manager) Remove(r RoleRef) (s Session, removed bool, err error) {
	s = session(r)

	if !Available() {
		return s, false, ErrNotInstalled
	}

	live, err := m.running()
	if err != nil {
		return s, false, err
	}
	if !live[s.Name] {
		return s, false, nil
	}

	if _, err := run(m.Socket, "kill-session", "-t", s.Name); err != nil {
		return s, false, err
	}

	return s, true, nil
}

// ServerAlive reports whether a tmux server answers on this socket. The socket
// file existing proves nothing: only a reply does.
func (m *Manager) ServerAlive() bool {
	if !Available() {
		return false
	}
	_, err := run(m.Socket, "list-sessions", "-F", "#{session_name}")
	if err == nil {
		return true
	}
	return !isNoServer(err)
}

// promptSettleDelay is how long to wait between typing text into a pane and
// pressing Enter.
//
// Interactive TUIs (Codex among them) read the pasted text asynchronously: an
// Enter delivered in the same instant arrives before the composer has taken the
// text, so the line sits there unsent and the agent never sees it. A short
// pause makes the submission reliable. It is only paid once per wake-up.
const promptSettleDelay = 400 * time.Millisecond

// SendPrompt types a line into a session's active pane and submits it.
//
// Use this rather than SendKeys when the target is an interactive program that
// needs the input actually entered, not merely typed.
func (m *Manager) SendPrompt(name, text string) error {
	if err := m.SendKeys(name, text); err != nil {
		return err
	}

	time.Sleep(promptSettleDelay)

	return m.SendKeys(name, "Enter")
}
