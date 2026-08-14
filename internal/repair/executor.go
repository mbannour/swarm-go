package repair

import (
	"fmt"
	"io"
	"time"
)

// Actuator performs the individual changes. Every method must be safe to call
// when the thing it fixes is already fine, and none of them may destroy work.
type Actuator interface {
	ClearDaemonMetadata() error
	StartDaemon() error
	RemoveStaleSocket() error
	CreateSession(role string) error
	StartAgent(role string) error
	CreateWorktree(role string) error
	PruneWorktrees() error
	// ReconcileOrphan retires an already-delivered handoff on the sender side
	// without redelivering it.
	ReconcileOrphan(role, id string) error
	CleanTempFiles() (int, error)
	// Integrate applies a role's pending handed-off commit. It must never
	// resolve a conflict: a failure is reported, not worked around.
	Integrate(role string) error
}

// Result is the outcome of one action.
type Result struct {
	Action Action
	Err    error
	// Note carries extra detail, such as how many files were removed.
	Note string
}

// OK reports whether the action succeeded.
func (r Result) OK() bool { return r.Err == nil }

// Report is the outcome of executing a plan.
type Report struct {
	Results []Result
	DryRun  bool
}

// Failed returns the actions that did not succeed.
func (r Report) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if !res.OK() {
			out = append(out, res)
		}
	}
	return out
}

// Executor applies a plan.
type Executor struct {
	Actuator Actuator
	// Log receives one line per action, with a timestamp and component.
	Log io.Writer
}

// Execute performs each action in order, continuing past a failure so one
// stuck component does not prevent the rest from being repaired.
func (e *Executor) Execute(plan Plan, dryRun bool) Report {
	report := Report{DryRun: dryRun}

	for _, action := range plan.Actions {
		if dryRun {
			report.Results = append(report.Results, Result{Action: action})
			continue
		}

		note, err := e.perform(action)
		report.Results = append(report.Results, Result{Action: action, Err: err, Note: note})

		e.logf(action.Component, "%s: %s", action.Kind, outcome(err))
	}

	return report
}

// perform dispatches one action to the actuator.
func (e *Executor) perform(a Action) (string, error) {
	switch a.Kind {
	case KindClearDaemonMetadata:
		return "", e.Actuator.ClearDaemonMetadata()

	case KindRemoveStaleSocket:
		return "", e.Actuator.RemoveStaleSocket()

	case KindStartDaemon:
		return "", e.Actuator.StartDaemon()

	case KindCreateSession:
		if err := e.Actuator.CreateSession(a.Component); err != nil {
			return "", err
		}
		// A recreated session is empty; its agent belongs with it.
		return "agent restarted", e.Actuator.StartAgent(a.Component)

	case KindStartAgent:
		return "", e.Actuator.StartAgent(a.Component)

	case KindCreateWorktree:
		return "", e.Actuator.CreateWorktree(a.Component)

	case KindPruneWorktrees:
		return "", e.Actuator.PruneWorktrees()

	case KindReconcileOrphan:
		id := a.Cause.Detail["id"]
		if id == "" {
			return "", fmt.Errorf("orphan diagnostic carries no handoff id")
		}
		return "", e.Actuator.ReconcileOrphan(a.Component, id)

	case KindIntegrate:
		return "", e.Actuator.Integrate(a.Component)

	case KindCleanTempFiles:
		n, err := e.Actuator.CleanTempFiles()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d file(s) removed", n), nil

	default:
		return "", fmt.Errorf("unknown repair action %q", a.Kind)
	}
}

func (e *Executor) logf(component, format string, args ...interface{}) {
	if e.Log == nil {
		return
	}
	fmt.Fprintf(e.Log, "%s  repair  %-12s %s\n",
		time.Now().Format("15:04:05"), component, fmt.Sprintf(format, args...))
}

func outcome(err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	return "ok"
}
