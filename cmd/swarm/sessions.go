package main

import (
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/tmux"
)

func runSessions(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm sessions <create|list|attach <role>|remove>")
		os.Exit(1)
	}

	if !tmux.Available() {
		fail(tmux.ErrNotInstalled)
	}

	cfg, wtMgr := loadWorktreeContext()
	mgr := tmux.NewManager(wtMgr.Root)

	refs, err := sessionRefs(cfg, wtMgr)
	if err != nil {
		fail(err)
	}

	switch args[0] {
	case "create":
		fail(sessionsCreate(cfg, mgr, refs))
	case "list":
		fail(sessionsList(mgr, refs))
	case "attach":
		if len(args) < 2 {
			fail(fmt.Errorf("usage: swarm sessions attach <role>"))
		}
		fail(sessionsAttach(mgr, refs, args[1]))
	case "remove":
		fail(sessionsRemove(mgr, refs))
	default:
		fmt.Printf("unknown sessions subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// sessionRefs pairs each configured role with the absolute path of its
// worktree, which is where its tmux session will start.
func sessionRefs(cfg *config.Config, wtMgr *git.WorktreeManager) ([]tmux.RoleRef, error) {
	refs := make([]tmux.RoleRef, 0, len(cfg.Roles))

	for _, r := range cfg.Roles {
		wt, err := wtMgr.Plan(r.Name, r.Worktree)
		if err != nil {
			return nil, fmt.Errorf("role %s: %w", r.Name, err)
		}
		refs = append(refs, tmux.RoleRef{Name: r.Name, WorkingDir: wt.AbsPath})
	}

	return refs, nil
}

// findRef resolves a role name from the current configuration.
func findRef(refs []tmux.RoleRef, role string) (tmux.RoleRef, error) {
	for _, r := range refs {
		if r.Name == role {
			return r, nil
		}
	}
	return tmux.RoleRef{}, fmt.Errorf("unknown role %q", role)
}

func sessionsCreate(cfg *config.Config, mgr *tmux.Manager, refs []tmux.RoleRef) error {
	fmt.Println("Creating four-pack sessions")
	fmt.Println()

	for _, r := range refs {
		s, created, err := mgr.Create(r)
		switch {
		case err != nil:
			return fmt.Errorf("create tmux session for %s: %w", r.Name, err)
		case created:
			fmt.Printf("✓ %-12s %s\n", r.Name, s.Name)
		default:
			fmt.Printf("○ %-12s already running\n", r.Name)
		}
	}

	return nil
}

func sessionsList(mgr *tmux.Manager, refs []tmux.RoleRef) error {
	statuses, err := mgr.List(refs)
	if err != nil {
		return err
	}

	fmt.Printf("%-12s %-20s %s\n", "ROLE", "SESSION", "STATUS")

	for _, s := range statuses {
		status := "missing"
		if s.Running {
			status = "running"
		}
		fmt.Printf("%-12s %-20s %s\n", s.Role, s.Name, status)
	}

	return nil
}

func sessionsAttach(mgr *tmux.Manager, refs []tmux.RoleRef, role string) error {
	ref, err := findRef(refs, role)
	if err != nil {
		return err
	}
	return mgr.Attach(ref)
}

func sessionsRemove(mgr *tmux.Manager, refs []tmux.RoleRef) error {
	fmt.Println("Removing four-pack sessions")
	fmt.Println()

	for _, r := range refs {
		s, removed, err := mgr.Remove(r)
		switch {
		case err != nil:
			return fmt.Errorf("remove tmux session for %s: %w", r.Name, err)
		case removed:
			fmt.Printf("✓ %-12s killed %s\n", r.Name, s.Name)
		default:
			fmt.Printf("○ %-12s not running\n", r.Name)
		}
	}

	return nil
}
