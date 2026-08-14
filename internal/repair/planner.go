// Package repair turns diagnostics into safe, deterministic changes.
//
// The split is deliberate: the planner decides what should happen from typed
// diagnostics, and the executor performs it. Nothing here parses formatted
// output, and nothing repairs an issue the detector marked unrepairable.
package repair

import (
	"fmt"

	"github.com/mbannour/swarm-go/internal/diagnostics"
)

// Kind is the sort of change a repair action makes.
type Kind string

const (
	KindClearDaemonMetadata Kind = "clear-daemon-metadata"
	KindStartDaemon         Kind = "start-daemon"
	KindRemoveStaleSocket   Kind = "remove-stale-socket"
	KindCreateSession       Kind = "create-session"
	KindStartAgent          Kind = "start-agent"
	KindCreateWorktree      Kind = "create-worktree"
	KindPruneWorktrees      Kind = "prune-worktrees"
	KindReconcileOrphan     Kind = "reconcile-orphan"
	KindCleanTempFiles      Kind = "clean-temp-files"
	KindIntegrate           Kind = "integrate"
)

// Action is one planned change.
type Action struct {
	Kind      Kind   `json:"kind"`
	Component string `json:"component"`
	// Description is the human-facing sentence for --dry-run and for output.
	Description string `json:"description"`
	// Cause links back to the diagnostic that justified this action.
	Cause diagnostics.Diagnostic `json:"cause"`
}

// Plan is an ordered set of actions plus what was deliberately left alone.
type Plan struct {
	Actions []Action
	// Blocked are the diagnostics a human must resolve.
	Blocked []diagnostics.Diagnostic
}

// Empty reports whether there is nothing to do.
func (p Plan) Empty() bool { return len(p.Actions) == 0 }

// PlanFrom turns a report into an ordered repair plan.
//
// Order matters and mirrors startup: clear stale metadata, then infrastructure
// (socket, worktrees, daemon), then sessions, then agents. Reconciliation of
// durable handoff state comes last, once delivery can actually happen again.
func PlanFrom(report diagnostics.Report) Plan {
	var plan Plan

	// One action per kind+component, in dependency order.
	byOrder := []struct {
		code string
		kind Kind
		text func(diagnostics.Diagnostic) string
	}{
		{diagnostics.CodeDaemonStalePID, KindClearDaemonMetadata, func(d diagnostics.Diagnostic) string {
			return "remove stale daemon PID metadata"
		}},
		{diagnostics.CodeSocketStale, KindRemoveStaleSocket, func(d diagnostics.Diagnostic) string {
			return "remove the stale tmux socket (no server answers through it)"
		}},
		{diagnostics.CodeWorktreeStale, KindPruneWorktrees, func(d diagnostics.Diagnostic) string {
			return "prune stale Git worktree metadata under .swarm/worktrees"
		}},
		{diagnostics.CodeWorktreeMissing, KindCreateWorktree, func(d diagnostics.Diagnostic) string {
			return fmt.Sprintf("recreate the %s worktree", d.Component)
		}},
		{diagnostics.CodeDaemonNotRunning, KindStartDaemon, func(d diagnostics.Diagnostic) string {
			return "restart the handoff daemon"
		}},
		{diagnostics.CodeSessionMissing, KindCreateSession, func(d diagnostics.Diagnostic) string {
			return fmt.Sprintf("recreate the %s tmux session", d.Component)
		}},
		{diagnostics.CodeAgentMissing, KindStartAgent, func(d diagnostics.Diagnostic) string {
			return fmt.Sprintf("restart the %s agent in its existing session", d.Component)
		}},
		{diagnostics.CodeOrphanDelivery, KindReconcileOrphan, func(d diagnostics.Diagnostic) string {
			return fmt.Sprintf("mark %s's handoff %s as sent (it was already delivered)",
				d.Component, shortID(d.Detail["id"]))
		}},
		{diagnostics.CodeIntegrationPend, KindIntegrate, func(d diagnostics.Diagnostic) string {
			return fmt.Sprintf("integrate %s's handed-off commit into its worktree", d.Component)
		}},
		{diagnostics.CodeTempFiles, KindCleanTempFiles, func(d diagnostics.Diagnostic) string {
			return "remove stale temporary files from the managed runtime directory"
		}},
	}

	seen := map[string]bool{}

	for _, step := range byOrder {
		for _, d := range report.Diagnostics {
			if d.Code != step.code || !d.Repairable {
				continue
			}

			key := string(step.kind) + "/" + d.Component
			if seen[key] {
				continue
			}
			seen[key] = true

			plan.Actions = append(plan.Actions, Action{
				Kind:        step.kind,
				Component:   d.Component,
				Description: step.text(d),
				Cause:       d,
			})
		}
	}

	// A session that is being recreated will get its agent started as part of
	// that step, so drop a redundant agent action for the same role.
	plan.Actions = dropRedundantAgentStarts(plan.Actions)

	plan.Blocked = report.Blocking()

	return plan
}

// dropRedundantAgentStarts removes start-agent when the session for the same
// role is being created in the same plan.
func dropRedundantAgentStarts(actions []Action) []Action {
	creating := map[string]bool{}
	for _, a := range actions {
		if a.Kind == KindCreateSession {
			creating[a.Component] = true
		}
	}

	out := actions[:0]
	for _, a := range actions {
		if a.Kind == KindStartAgent && creating[a.Component] {
			continue
		}
		out = append(out, a)
	}

	return out
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
