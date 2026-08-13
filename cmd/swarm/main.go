package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/roles"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {

	case "start":
		runStart(os.Args[2:])

	case "stop":
		runStop(os.Args[2:])

	case "logs":
		runLogs(os.Args[2:])

	case "task":
		runTask(os.Args[2:])

	case "config":
		printConfig()

	case "version":
		fmt.Println("swarm-go v0.1.0")

	case "roles":
		printRoles()

	case "doctor":
		runDoctor()

	case "worktrees":
		runWorktrees(os.Args[2:])

	case "sessions":
		runSessions(os.Args[2:])

	case "agents":
		runAgents(os.Args[2:])

	case "handoff":
		runHandoff(os.Args[2:])

	case "status":
		runStatus(os.Args[2:])

	default:
		fmt.Printf("unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printConfig() {
	cfg, err := config.Load("swarm.conf")
	if err != nil {
		fmt.Printf("failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	if err := cfg.ValidateFourPack(); err != nil {
		fmt.Printf("invalid four-pack configuration: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Four-pack configuration")
	fmt.Println()

	fmt.Printf(
		"%-12s %-10s %-16s %-10s\n",
		"ROLE",
		"BACKEND",
		"WORKTREE",
		"MODE",
	)

	for _, role := range cfg.Roles {
		fmt.Printf(
			"%-12s %-10s %-16s %-10s\n",
			role.Name,
			role.Backend,
			role.Worktree,
			role.ReceiveMode,
		)
	}
}

func printUsage() {
	fmt.Println(`Usage:
  swarm start                 bring the whole four-pack up
  swarm status [--json]       read-only overview
  swarm stop                  stop processes, keep durable state
  swarm logs daemon
  swarm task submit --id <id> --description <text>
  swarm task trace <id>

  swarm version
  swarm roles
  swarm doctor
  swarm config
  swarm worktrees list
  swarm worktrees create
  swarm worktrees remove [--force]
  swarm sessions list
  swarm sessions create
  swarm sessions attach <role>
  swarm sessions remove
  swarm agents start [role]
  swarm agents list
  swarm agents stop [role]
  swarm handoff send --from <role> --to <role> --type <type> --note <text> [...]
  swarm handoff inbox <role>
  swarm handoff outbox <role>
  swarm handoff ready <role>
  swarm handoff current <role>
  swarm handoff status <role>
  swarm handoff next --from <role> [...]
  swarm handoff done <role>
  swarm handoff daemon
  swarm status`)
}

func printRoles() {
	fmt.Println("Four-pack roles:")

	for i, role := range roles.FourPack() {
		fmt.Printf("  %d. %s\n", i+1, role.Name)
	}
}

func runDoctor() {
	fmt.Println("Swarm environment check")
	fmt.Println()

	checkCommand("git")
	checkCommand("tmux")

	fmt.Println()
	fmt.Println("Agent backends:")

	checkOptionalCommand("codex")
	checkOptionalCommand("claude")
}

func checkCommand(name string) {
	path, err := exec.LookPath(name)

	if err != nil {
		fmt.Printf("✗ %-10s not found\n", name)
		return
	}

	fmt.Printf("✓ %-10s %s\n", name, path)
}

func checkOptionalCommand(name string) {
	path, err := exec.LookPath(name)

	if err != nil {
		fmt.Printf("○ %-10s not installed\n", name)
		return
	}

	fmt.Printf("✓ %-10s %s\n", name, path)
}
