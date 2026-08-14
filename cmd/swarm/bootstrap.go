package main

import (
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/agent"
)

// runBootstrap prepares each configured backend for unattended operation.
//
// It is a separate, explicit command because what it does is a security
// decision: telling Codex to trust this repository is exactly what answering
// its own prompt would do, and swarm will not do that behind your back during
// `start`.
func runBootstrap(args []string) {
	cfg, wtMgr := loadWorktreeContext()
	repoRoot := wtMgr.Root

	fmt.Println("Swarm bootstrap")
	fmt.Println()
	fmt.Printf("Repository\n  %s\n\n", repoRoot)

	seen := map[string]bool{}
	blocked := 0

	for _, r := range cfg.Roles {
		policy := agent.Approval(r.Approval)

		b, ok := agent.BootstrapperFor(r.Backend)
		if !ok {
			fmt.Printf("○ %-12s %-8s nothing to prepare\n", r.Name, r.Backend)
			continue
		}

		// Trust is recorded per repository, so one backend needs one action.
		if !seen[r.Backend] {
			seen[r.Backend] = true

			changed, err := b.Bootstrap(repoRoot, policy)
			switch {
			case err != nil:
				fmt.Printf("✗ %-12s %-8s %v\n", r.Name, r.Backend, err)
				blocked++
				continue
			case changed:
				fmt.Printf("✓ %-8s workspace recorded as trusted\n", r.Backend)
			default:
				fmt.Printf("○ %-8s workspace already trusted\n", r.Backend)
			}
		}

		state, reason := b.Ready(repoRoot, policy)
		if state == agent.ReadinessReady {
			fmt.Printf("✓ %-12s %-8s ready (approval: %s)\n", r.Name, r.Backend, r.Approval)
		} else {
			fmt.Printf("✗ %-12s %-8s %s: %s\n", r.Name, r.Backend, state, reason)
			blocked++
		}
	}

	fmt.Println()

	if blocked > 0 {
		fmt.Printf("NOT READY (%d role(s) blocked)\n", blocked)
		os.Exit(1)
	}

	fmt.Println("READY")
}
