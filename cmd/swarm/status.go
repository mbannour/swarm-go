package main

import (
	"fmt"

	"github.com/mbannour/swarm-go/internal/agent"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// runStatus prints one read-only overview of the whole four-pack. It is the
// human-facing summary; agents use the machine-readable handoff commands.
func runStatus() {
	cfg, wtMgr := loadWorktreeContext()
	roleNames := configuredRoles(cfg)

	store := handoff.NewStore(wtMgr.Root, handoff.NewRoles(roleNames))
	life := handoff.NewLifecycle(store, receiveModeLookup(cfg))

	var (
		tmuxMgr  *tmux.Manager
		agentMgr *agent.Manager
	)
	if tmux.Available() {
		tmuxMgr = tmux.NewManager(wtMgr.Root)
		agentMgr = agent.NewManager(wtMgr.Root, tmuxMgr)
	}

	fmt.Println("FOUR-PACK STATUS")
	fmt.Println()
	fmt.Printf("%-12s %-16s %-10s %-9s %s\n", "ROLE", "AGENT", "WORK", "INBOX", "TASK")

	roles, err := agentRoles(cfg, wtMgr)
	if err != nil {
		fail(err)
	}

	for _, r := range roles {
		agentState := "unknown"
		if agentMgr != nil {
			state, err := agentMgr.Status(r)
			if err != nil {
				state = "unknown"
			}
			agentState = string(state)
		} else {
			agentState = "no-tmux"
		}

		status, err := life.Status(r.Name)
		if err != nil {
			fail(err)
		}

		task := "-"
		if len(status.Current) > 0 {
			task = status.Current[0].Task
			if task == "" {
				task = string(status.Current[0].Type)
			}
			if status.DownstreamSent {
				task += " (handed off)"
			}
		}

		fmt.Printf("%-12s %-16s %-10s %-9d %s\n",
			r.Name, agentState, status.State(), status.Inbox, task)
	}

	fmt.Println()
	fmt.Println("ROUTE")
	for _, hop := range handoff.Route() {
		fmt.Printf("  %s -> %s\n", hop[0], hop[1])
	}

	fmt.Println()
	fmt.Println("PENDING DELIVERY")

	pending := 0
	for _, name := range roleNames {
		out, err := store.Outbox(name)
		if err != nil {
			fail(err)
		}
		if len(out) > 0 {
			fmt.Printf("  %-12s %d queued in outbox\n", name, len(out))
			pending += len(out)
		}
	}
	if pending == 0 {
		fmt.Println("  none")
	} else {
		fmt.Println()
		fmt.Println("  run `swarm handoff daemon` to deliver these")
	}
}
