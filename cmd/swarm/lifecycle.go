package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/agent"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/lifecycle"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// newLifecycleManager wires the real components into the lifecycle.
func newLifecycleManager() *lifecycle.Manager {
	cfg, wtMgr := loadWorktreeContext()
	repoRoot := wtMgr.Root
	roleNames := configuredRoles(cfg)

	roles := make([]lifecycle.Role, 0, len(cfg.Roles))
	for _, r := range cfg.Roles {
		wt, err := wtMgr.Plan(r.Name, r.Worktree)
		if err != nil {
			fail(fmt.Errorf("role %s: %w", r.Name, err))
		}
		roles = append(roles, lifecycle.Role{
			Name:         r.Name,
			Backend:      r.Backend,
			WorktreeName: r.Worktree,
			Worktree:     wt.AbsPath,
			Branch:       wt.Branch,
			ReceiveMode:  string(r.ReceiveMode),
		})
	}

	tmuxMgr := tmux.NewManager(repoRoot)
	store := handoff.NewStore(repoRoot, handoff.NewRoles(roleNames))
	life := handoff.NewLifecycle(store, receiveModeLookup(cfg))

	// A missing binary is only fatal when something needs to launch; status
	// must still work, so resolution failure is left to the step that needs it.
	swarmBin, _ := agent.ResolveBinary(repoRoot)

	return &lifecycle.Manager{
		RepoRoot:  repoRoot,
		Roles:     roles,
		Worktrees: lifecycle.GitWorktrees{Mgr: wtMgr},
		Sessions:  lifecycle.TmuxSessions{Mgr: tmuxMgr},
		Agents: lifecycle.CodingAgents{
			Mgr:      agent.NewManager(repoRoot, tmuxMgr),
			RepoRoot: repoRoot,
			SwarmBin: swarmBin,
		},
		Work: lifecycle.HandoffWork{Store: store, Life: life, Roles: roleNames},
		Env:  lifecycle.HostEnvironment{RepoRoot: repoRoot},
		Out:  os.Stdout,
	}
}

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "show every step")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}

	mgr := newLifecycleManager()
	mgr.Verbose = *verbose

	fmt.Println("Swarm preflight")
	fmt.Println()

	report, err := mgr.Start(context.Background())

	for _, c := range report.Checks {
		if c.OK() {
			fmt.Printf("✓ %s\n", c.Name)
		} else {
			fmt.Printf("✗ %s: %v\n", c.Name, c.Err)
		}
	}

	if err != nil && len(report.Steps) == 0 {
		fmt.Println()
		fmt.Fprintln(os.Stderr, "swarm start aborted")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Starting swarm")
	fmt.Println()

	for _, s := range report.Steps {
		switch {
		case s.Err != nil:
			fmt.Printf("✗ %-22s %v\n", s.Name, s.Err)
		case s.Created:
			fmt.Printf("✓ %-22s started\n", s.Name)
		default:
			fmt.Printf("○ %-22s already running\n", s.Name)
		}
	}

	fmt.Println()

	if err != nil {
		fmt.Println("Swarm startup incomplete.")
		fmt.Println()
		printStatus(report.Status)
		fmt.Println()
		fmt.Println("Fix the reported problem, then run `swarm start` again to repair it.")
		os.Exit(1)
	}

	if report.AlreadyUp {
		fmt.Println("Swarm was already running.")
	} else {
		fmt.Println("Swarm is ready.")
	}

	fmt.Println()
	printRoleTable(report.Status)

	fmt.Println()
	fmt.Println("Next:")
	fmt.Println("  swarm status")
	fmt.Println("  swarm sessions attach coder")
	fmt.Println("  swarm stop")
}

func runStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "show every step")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}

	mgr := newLifecycleManager()
	mgr.Verbose = *verbose

	fmt.Println("Stopping swarm")
	fmt.Println()

	report, err := mgr.Stop(context.Background())

	for _, s := range report.Steps {
		switch {
		case s.Err != nil:
			fmt.Printf("✗ %-22s %v\n", s.Name, s.Err)
		case s.Created:
			fmt.Printf("✓ %-22s stopped\n", s.Name)
		default:
			fmt.Printf("○ %-22s was not running\n", s.Name)
		}
	}

	fmt.Println()

	if err != nil {
		fail(err)
	}

	if report.AlreadyOff {
		fmt.Println("Swarm was already stopped.")
	} else {
		fmt.Println("Swarm stopped.")
	}

	fmt.Println()
	fmt.Println("Worktrees, branches and handoffs were preserved.")
	fmt.Println("`swarm start` resumes any work in progress; `swarm worktrees remove` clears the worktrees.")
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print machine-readable status")
	strict := fs.Bool("strict", false, "exit non-zero unless the swarm is healthy")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}

	mgr := newLifecycleManager()

	status, err := mgr.Status(context.Background())
	if err != nil {
		fail(err)
	}

	if *asJSON {
		data, err := status.JSON()
		if err != nil {
			fail(err)
		}
		fmt.Println(string(data))
	} else {
		printStatus(status)
	}

	// Plain `status` is an observation and succeeds; --strict is the switch CI
	// uses to turn an unhealthy swarm into a failure.
	if *strict && status.Health != lifecycle.HealthHealthy {
		os.Exit(1)
	}
}

func printStatus(s lifecycle.SwarmStatus) {
	fmt.Println("Swarm")
	fmt.Println()
	fmt.Println("Repository")
	fmt.Printf("  %s\n", s.Repository)
	fmt.Println()

	fmt.Println("Handoff daemon")
	if s.Daemon.PID > 0 {
		fmt.Printf("  %s (pid %d)\n", s.Daemon.State, s.Daemon.PID)
	} else {
		fmt.Printf("  %s\n", s.Daemon.State)
	}
	fmt.Println()

	printRoleTable(s)

	fmt.Println()
	fmt.Println("HANDOFFS")
	fmt.Printf("  inbox      %d\n", s.Handoffs.Inbox)
	fmt.Printf("  current    %d\n", s.Handoffs.Current)
	fmt.Printf("  outbox     %d\n", s.Handoffs.Outbox)
	fmt.Printf("  completed  %d\n", s.Handoffs.Completed)
	fmt.Printf("  failed     %d\n", s.Handoffs.Failed)
	fmt.Printf("  rejected   %d\n", s.Handoffs.Rejected)

	fmt.Println()
	fmt.Println("STATUS")
	fmt.Printf("  %s\n", s.Health)
}

func printRoleTable(s lifecycle.SwarmStatus) {
	fmt.Printf("%-12s %-10s %-10s %-10s %-10s %s\n",
		"ROLE", "WORKTREE", "SESSION", "AGENT", "WORK", "TASK")

	for _, r := range s.Roles {
		task := r.Task
		if task == "" {
			task = "-"
		}
		fmt.Printf("%-12s %-10s %-10s %-10s %-10s %s\n",
			r.Role, r.Worktree, r.Session, r.Agent, r.Work, task)
	}
}

func runLogs(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm logs daemon")
		os.Exit(1)
	}

	_, wtMgr := loadWorktreeContext()

	switch args[0] {
	case "daemon", "handoffd":
		out, err := lifecycle.DaemonLogTail(wtMgr.Root, 64*1024)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	default:
		// Agents run interactively inside tmux, so their output lives in the
		// pane's scrollback rather than in a managed file.
		fail(fmt.Errorf(
			"only the daemon writes a managed log; for %s use `swarm sessions attach %s`",
			args[0], args[0],
		))
	}
}
