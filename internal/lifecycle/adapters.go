package lifecycle

import (
	"fmt"
	"os/exec"

	"github.com/mbannour/swarm-go/internal/agent"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/notify"
	"github.com/mbannour/swarm-go/internal/prompt"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// The adapters below are the only place the lifecycle touches concrete
// components. Each one is a thin translation, never new behavior.

// GitWorktrees adapts internal/git.
type GitWorktrees struct {
	Mgr *git.WorktreeManager
}

func (g GitWorktrees) Ensure(r Role) (bool, error) {
	_, created, err := g.Mgr.Create(r.Name, r.WorktreeName)
	return created, err
}

func (g GitWorktrees) Present(r Role) (bool, error) {
	wt, err := g.Mgr.Plan(r.Name, r.WorktreeName)
	if err != nil {
		return false, err
	}
	return g.Mgr.Exists(wt)
}

// TmuxSessions adapts internal/tmux.
type TmuxSessions struct {
	Mgr *tmux.Manager
}

func (t TmuxSessions) ref(r Role) tmux.RoleRef {
	return tmux.RoleRef{Name: r.Name, WorkingDir: r.Worktree}
}

func (t TmuxSessions) Ensure(r Role) (bool, error) {
	_, created, err := t.Mgr.Create(t.ref(r))
	return created, err
}

func (t TmuxSessions) Present(r Role) (bool, error) {
	return t.Mgr.HasSession(tmux.SessionName(r.Name))
}

func (t TmuxSessions) Remove(r Role) (bool, error) {
	_, removed, err := t.Mgr.Remove(t.ref(r))
	return removed, err
}

// CodingAgents adapts internal/agent, assembling each role's prompt on demand.
type CodingAgents struct {
	Mgr      *agent.Manager
	RepoRoot string
	SwarmBin string
}

func (c CodingAgents) role(r Role) agent.Role {
	return agent.Role{
		Name:        r.Name,
		Backend:     r.Backend,
		Worktree:    r.Worktree,
		Branch:      r.Branch,
		ReceiveMode: r.ReceiveMode,
		Approval:    agent.Approval(r.Approval),
	}
}

func (c CodingAgents) Ensure(r Role) (bool, error) {
	set, err := prompt.LoadForRole(c.RepoRoot, r.Name)
	if err != nil {
		return false, fmt.Errorf("load prompt for %s: %w", r.Name, err)
	}

	next, _ := handoff.NextRole(r.Name)

	assembled := prompt.Assemble(set, prompt.RuntimeContext{
		Role:        r.Name,
		RepoRoot:    c.RepoRoot,
		Worktree:    r.Worktree,
		Branch:      r.Branch,
		ReceiveMode: r.ReceiveMode,
		NextRole:    next,
		SwarmBin:    c.SwarmBin,
	})

	return c.Mgr.Start(c.role(r), assembled)
}

func (c CodingAgents) Stop(r Role) (bool, error) {
	return c.Mgr.Stop(c.role(r))
}

func (c CodingAgents) State(r Role) (string, error) {
	state, err := c.Mgr.Status(c.role(r))
	return string(state), err
}

// HandoffWork adapts the durable handoff lifecycle.
type HandoffWork struct {
	Store  *handoff.Store
	Life   *handoff.Lifecycle
	Roles  []string
	Notify *notify.Tracker
}

// Notification reports the last wake-up attempt for a role.
func (h HandoffWork) Notification(role string) (string, int, string) {
	if h.Notify == nil {
		return string(notify.StatusNotRequired), 0, ""
	}

	state := h.Notify.State(role)

	return string(state.Status), state.Attempts, state.LastError
}

func (h HandoffWork) Work(role string) (string, string, error) {
	status, err := h.Life.Status(role)
	if err != nil {
		return "unknown", "", err
	}

	task := ""
	if len(status.Current) > 0 {
		task = status.Current[0].Task
		if task == "" {
			task = string(status.Current[0].Type)
		}
	}

	return status.State(), task, nil
}

func (h HandoffWork) Counts() (Counts, error) {
	var counts Counts

	for _, role := range h.Roles {
		for _, box := range []struct {
			name  string
			total *int
		}{
			{handoff.BoxInbox, &counts.Inbox},
			{handoff.BoxCurrent, &counts.Current},
			{handoff.BoxOutbox, &counts.Outbox},
			{handoff.BoxCompleted, &counts.Completed},
			{handoff.BoxFailed, &counts.Failed},
		} {
			entries, err := h.Store.List(role, box.name)
			if err != nil {
				return counts, err
			}
			*box.total += len(entries)
		}
	}

	rejected, _, err := h.Store.ListDir(h.Store.RejectedDir())
	if err != nil {
		return counts, err
	}
	counts.Rejected = len(rejected)

	return counts, nil
}

// HostEnvironment answers preflight questions about this machine.
type HostEnvironment struct {
	RepoRoot string
}

func (e HostEnvironment) TmuxAvailable() bool { return tmux.Available() }

func (e HostEnvironment) BackendAvailable(backend string) bool {
	b, err := agent.Lookup(backend)
	if err != nil {
		return false
	}
	_, lookErr := exec.LookPath(b.Executable())
	return lookErr == nil
}

func (e HostEnvironment) PromptsPresent(role string) error {
	_, err := prompt.LoadForRole(e.RepoRoot, role)
	return err
}

func (e HostEnvironment) SwarmBinary() (string, error) {
	return agent.ResolveBinary(e.RepoRoot)
}

// BackendReady asks the backend itself whether it can run unattended here.
func (e HostEnvironment) BackendReady(backend, approval string) (string, string) {
	b, ok := agent.BootstrapperFor(backend)
	if !ok {
		return "ready", "" // nothing to prepare
	}

	state, reason := b.Ready(e.RepoRoot, agent.Approval(approval))

	return string(state), reason
}
