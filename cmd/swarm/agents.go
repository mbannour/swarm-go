package main

import (
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/agent"
	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/prompt"
	"github.com/mbannour/swarm-go/internal/tmux"
)

func runAgents(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm agents <start [role]|list|stop [role]>")
		os.Exit(1)
	}

	if !tmux.Available() {
		fail(tmux.ErrNotInstalled)
	}

	cfg, wtMgr := loadWorktreeContext()
	tmuxMgr := tmux.NewManager(wtMgr.Root)
	mgr := agent.NewManager(wtMgr.Root, tmuxMgr)

	roles, err := agentRoles(cfg, wtMgr)
	if err != nil {
		fail(err)
	}

	// An optional role argument narrows the command to a single role.
	if len(args) > 1 {
		roles, err = filterRole(roles, args[1])
		if err != nil {
			fail(err)
		}
	}

	switch args[0] {
	case "start":
		fail(agentsStart(mgr, wtMgr.Root, roles))
	case "list":
		fail(agentsList(mgr, roles))
	case "stop":
		fail(agentsStop(mgr, roles))
	default:
		fmt.Printf("unknown agents subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// agentRoles resolves each configured role to its worktree and branch.
func agentRoles(cfg *config.Config, wtMgr *git.WorktreeManager) ([]agent.Role, error) {
	out := make([]agent.Role, 0, len(cfg.Roles))

	for _, r := range cfg.Roles {
		wt, err := wtMgr.Plan(r.Name, r.Worktree)
		if err != nil {
			return nil, fmt.Errorf("role %s: %w", r.Name, err)
		}
		out = append(out, agent.Role{
			Name:        r.Name,
			Backend:     r.Backend,
			Worktree:    wt.AbsPath,
			Branch:      wt.Branch,
			ReceiveMode: string(r.ReceiveMode),
		})
	}

	return out, nil
}

func filterRole(roles []agent.Role, name string) ([]agent.Role, error) {
	for _, r := range roles {
		if r.Name == name {
			return []agent.Role{r}, nil
		}
	}
	return nil, fmt.Errorf("unknown role %q", name)
}

func agentsStart(mgr *agent.Manager, repoRoot string, roles []agent.Role) error {
	fmt.Println("Starting four-pack agents")
	fmt.Println()

	for _, r := range roles {
		set, err := prompt.LoadForRole(repoRoot, r.Name)
		if err != nil {
			return fmt.Errorf("load prompt for %s: %w", r.Name, err)
		}

		assembled := prompt.Assemble(set, prompt.RuntimeContext{
			Role:        r.Name,
			RepoRoot:    repoRoot,
			Worktree:    r.Worktree,
			Branch:      r.Branch,
			ReceiveMode: r.ReceiveMode,
		})

		started, err := mgr.Start(r, assembled)
		switch {
		case err != nil:
			return fmt.Errorf("start agent for %s: %w", r.Name, err)
		case started:
			fmt.Printf("✓ %-12s %-10s %s\n", r.Name, r.Backend, tmux.SessionName(r.Name))
		default:
			fmt.Printf("○ %-12s already running\n", r.Name)
		}
	}

	return nil
}

func agentsList(mgr *agent.Manager, roles []agent.Role) error {
	fmt.Printf("%-12s %-10s %-20s %s\n", "ROLE", "BACKEND", "SESSION", "STATUS")

	for _, r := range roles {
		state, err := mgr.Status(r)
		if err != nil {
			return err
		}
		fmt.Printf("%-12s %-10s %-20s %s\n", r.Name, r.Backend, tmux.SessionName(r.Name), state)
	}

	return nil
}

func agentsStop(mgr *agent.Manager, roles []agent.Role) error {
	fmt.Println("Stopping four-pack agents")
	fmt.Println()

	for _, r := range roles {
		stopped, err := mgr.Stop(r)
		switch {
		case err != nil:
			return fmt.Errorf("stop agent for %s: %w", r.Name, err)
		case stopped:
			fmt.Printf("✓ %-12s interrupted\n", r.Name)
		default:
			fmt.Printf("○ %-12s not running\n", r.Name)
		}
	}

	fmt.Println()
	fmt.Println("tmux sessions were left running; use `swarm sessions remove` to close them.")

	return nil
}
