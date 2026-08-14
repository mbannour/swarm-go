package main

import (
	"errors"
	"fmt"

	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
)

// gitIntegrator adapts the git package to the handoff lifecycle's interface,
// so the handoff package never issues Git commands itself.
type gitIntegrator struct {
	Integrator *git.Integrator
}

func (g gitIntegrator) Integrate(worktree, branch, commit string) (string, string, bool, error) {
	result, err := g.Integrator.Integrate(worktree, branch, commit)
	if err != nil {
		return "", "", false, err
	}
	return result.Method, result.LocalCommit, result.Already, nil
}

// handoffIntegrate applies the commit named by a role's current work to that
// role's own worktree. Output is machine-readable: agents run this.
func handoffIntegrate(life *handoff.Lifecycle, roles []roleTarget, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff integrate <role>")
	}
	role := args[0]

	target, err := findRoleTarget(roles, role)
	if err != nil {
		return err
	}

	// The worktree and branch come from configuration, never from the message.
	result, err := life.Integrate(role, target.Worktree, target.Branch, gitIntegrator{git.NewIntegrator()})
	if err != nil {
		var conflict *git.ConflictError
		if errors.As(err, &conflict) {
			fmt.Println("INTEGRATION_CONFLICT")
			fmt.Printf("ROLE: %s\n", role)
			fmt.Printf("COMMIT: %s\n", conflict.Commit)
			for _, f := range conflict.Files {
				fmt.Printf("CONFLICTED_FILE: %s\n", f)
			}
			fmt.Println("STATE: cherry-pick aborted; the worktree is unchanged")
			return fmt.Errorf("cannot integrate commit %s into %s: cherry-pick conflict", conflict.Commit, role)
		}
		return fmt.Errorf("cannot integrate handoff into %s: %w", role, err)
	}

	if !result.Required {
		fmt.Println("NO_GIT_INTEGRATION_REQUIRED")
		fmt.Printf("ROLE: %s\n", role)
		fmt.Printf("TYPE: %s\n", result.Entry.Type)
		return nil
	}

	if result.Already {
		fmt.Println("ALREADY_INTEGRATED")
	} else {
		fmt.Println("INTEGRATED")
	}

	fmt.Printf("ROLE: %s\n", role)
	fmt.Printf("COMMIT: %s\n", result.SourceCommit)
	fmt.Printf("LOCAL_COMMIT: %s\n", result.LocalCommit)
	fmt.Printf("METHOD: %s\n", result.Method)

	return nil
}

// roleTarget is a role's configured worktree and branch.
type roleTarget struct {
	Name     string
	Worktree string
	Branch   string
}

func findRoleTarget(roles []roleTarget, name string) (roleTarget, error) {
	for _, r := range roles {
		if r.Name == name {
			return r, nil
		}
	}
	return roleTarget{}, fmt.Errorf("unknown role %q", name)
}
