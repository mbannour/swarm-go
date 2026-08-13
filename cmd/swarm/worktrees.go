package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/git"
)

func runWorktrees(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm worktrees <list|create|remove>")
		os.Exit(1)
	}

	force := false
	for _, a := range args[1:] {
		if a == "--force" {
			force = true
			continue
		}
		fail(fmt.Errorf("unknown flag: %s", a))
	}

	cfg, mgr := loadWorktreeContext()

	switch args[0] {
	case "list":
		fail(worktreesList(cfg, mgr))
	case "create":
		fail(worktreesCreate(cfg, mgr))
	case "remove":
		fail(worktreesRemove(cfg, mgr, force))
	default:
		fmt.Printf("unknown worktrees subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// loadWorktreeContext loads swarm.conf and locates the enclosing repository.
func loadWorktreeContext() (*config.Config, *git.WorktreeManager) {
	cfg, err := config.Load("swarm.conf")
	if err != nil {
		fail(fmt.Errorf("failed to load configuration: %w", err))
	}

	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}

	mgr, err := git.NewManager(cwd)
	if err != nil {
		fail(err)
	}

	return cfg, mgr
}

// roleRefs adapts the configured roles to what the git package needs.
func roleRefs(cfg *config.Config) []git.RoleRef {
	refs := make([]git.RoleRef, 0, len(cfg.Roles))
	for _, r := range cfg.Roles {
		refs = append(refs, git.RoleRef{Name: r.Name, Worktree: r.Worktree})
	}
	return refs
}

func worktreesList(cfg *config.Config, mgr *git.WorktreeManager) error {
	statuses, err := mgr.List(roleRefs(cfg))
	if err != nil {
		return err
	}

	fmt.Printf("%-12s %-20s %-32s %s\n", "ROLE", "BRANCH", "WORKTREE", "STATUS")

	for _, s := range statuses {
		status := "missing"
		if s.Present {
			status = "ok"
		}
		fmt.Printf("%-12s %-20s %-32s %s\n", s.Role, s.Branch, s.RelPath, status)
	}

	return nil
}

func worktreesCreate(cfg *config.Config, mgr *git.WorktreeManager) error {
	fmt.Println("Creating four-pack worktrees")
	fmt.Println()

	for _, role := range cfg.Roles {
		wt, created, err := mgr.Create(role.Name, role.Worktree)
		switch {
		case errors.Is(err, git.ErrNoCommits):
			return fmt.Errorf("cannot create worktrees: repository has no commits yet\ncreate an initial commit first")
		case err != nil:
			return fmt.Errorf("create worktree for %s: %w", role.Name, err)
		case created:
			fmt.Printf("✓ %-12s %s\n", role.Name, wt.RelPath)
		default:
			fmt.Printf("○ %-12s already exists\n", role.Name)
		}
	}

	return nil
}

func worktreesRemove(cfg *config.Config, mgr *git.WorktreeManager, force bool) error {
	fmt.Println("Removing four-pack worktrees")
	fmt.Println()

	for _, role := range cfg.Roles {
		wt, removed, err := mgr.Remove(role.Name, role.Worktree, force)
		switch {
		case err != nil:
			return fmt.Errorf("remove worktree for %s: %w", role.Name, err)
		case removed:
			fmt.Printf("✓ %-12s removed %s\n", role.Name, wt.RelPath)
		default:
			fmt.Printf("○ %-12s not present\n", role.Name)
		}
	}

	fmt.Println()
	fmt.Println("Branches are kept; delete them with `git branch -D swarm/<role>` if you want a clean slate.")

	return nil
}

func fail(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%v\n", err)
	os.Exit(1)
}
