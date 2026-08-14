package repair

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mbannour/swarm-go/internal/diagnostics"
)

// fakeActuator records what was done to it.
type fakeActuator struct {
	calls []string
	errs  map[string]error
	tempN int
}

func newFakeActuator() *fakeActuator {
	return &fakeActuator{errs: map[string]error{}, tempN: 3}
}

func (f *fakeActuator) record(call string) error {
	f.calls = append(f.calls, call)
	return f.errs[call]
}

func (f *fakeActuator) ClearDaemonMetadata() error       { return f.record("clear-metadata") }
func (f *fakeActuator) StartDaemon() error               { return f.record("start-daemon") }
func (f *fakeActuator) RemoveStaleSocket() error         { return f.record("remove-socket") }
func (f *fakeActuator) CreateSession(role string) error  { return f.record("session:" + role) }
func (f *fakeActuator) StartAgent(role string) error     { return f.record("agent:" + role) }
func (f *fakeActuator) CreateWorktree(role string) error { return f.record("worktree:" + role) }
func (f *fakeActuator) PruneWorktrees() error            { return f.record("prune") }
func (f *fakeActuator) ReconcileOrphan(role, id string) error {
	return f.record("reconcile:" + role + ":" + id)
}
func (f *fakeActuator) CleanTempFiles() (int, error) {
	return f.tempN, f.record("clean-temp")
}

func report(diags ...diagnostics.Diagnostic) diagnostics.Report {
	return diagnostics.Report{Health: diagnostics.HealthDegraded, Diagnostics: diags}
}

func repairable(code, component string) diagnostics.Diagnostic {
	return diagnostics.Diagnostic{
		Code: code, Component: component, Severity: diagnostics.SeverityError, Repairable: true,
	}
}

func TestPlanSkipsUnrepairableDiagnostics(t *testing.T) {
	plan := PlanFrom(report(
		diagnostics.Diagnostic{
			Code: diagnostics.CodeWorktreeDirty, Component: "coder",
			Severity: diagnostics.SeverityWarning, Repairable: false,
		},
		diagnostics.Diagnostic{
			Code: diagnostics.CodeCurrentCorrupt, Component: "coder",
			Severity: diagnostics.SeverityCritical, Repairable: false,
		},
	))

	if !plan.Empty() {
		t.Fatalf("plan acts on unrepairable diagnostics: %+v", plan.Actions)
	}
	if len(plan.Blocked) != 1 {
		t.Errorf("blocked = %+v, want the critical diagnostic", plan.Blocked)
	}
}

// Infrastructure must be repaired before the things that depend on it.
func TestPlanOrdersActionsByDependency(t *testing.T) {
	plan := PlanFrom(report(
		repairable(diagnostics.CodeAgentMissing, "architect"),
		repairable(diagnostics.CodeSessionMissing, "refactorer"),
		repairable(diagnostics.CodeDaemonNotRunning, "daemon"),
		repairable(diagnostics.CodeWorktreeMissing, "refactorer"),
		repairable(diagnostics.CodeDaemonStalePID, "daemon"),
	))

	var order []Kind
	for _, a := range plan.Actions {
		order = append(order, a.Kind)
	}

	want := []Kind{
		KindClearDaemonMetadata,
		KindCreateWorktree,
		KindStartDaemon,
		KindCreateSession,
		KindStartAgent,
	}

	if len(order) != len(want) {
		t.Fatalf("plan = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("plan = %v, want %v", order, want)
		}
	}
}

// Recreating a session brings its agent with it; a separate action is noise.
func TestPlanDropsRedundantAgentStart(t *testing.T) {
	plan := PlanFrom(report(
		repairable(diagnostics.CodeSessionMissing, "coder"),
		repairable(diagnostics.CodeAgentMissing, "coder"),
	))

	for _, a := range plan.Actions {
		if a.Kind == KindStartAgent {
			t.Errorf("plan starts the agent separately from its new session: %+v", plan.Actions)
		}
	}
	if len(plan.Actions) != 1 {
		t.Errorf("plan = %+v, want one action", plan.Actions)
	}
}

func TestExecuteAppliesActions(t *testing.T) {
	actuator := newFakeActuator()
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	plan := PlanFrom(report(
		repairable(diagnostics.CodeDaemonNotRunning, "daemon"),
		repairable(diagnostics.CodeSessionMissing, "refactorer"),
	))

	result := executor.Execute(plan, false)

	if len(result.Failed()) != 0 {
		t.Errorf("failures: %+v", result.Failed())
	}

	joined := strings.Join(actuator.calls, " ")
	for _, want := range []string{"start-daemon", "session:refactorer", "agent:refactorer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("actuator was not asked to %q; calls: %v", want, actuator.calls)
		}
	}
}

// A dry run must change nothing at all.
func TestDryRunTouchesNothing(t *testing.T) {
	actuator := newFakeActuator()
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	plan := PlanFrom(report(
		repairable(diagnostics.CodeDaemonNotRunning, "daemon"),
		repairable(diagnostics.CodeSessionMissing, "coder"),
		repairable(diagnostics.CodeTempFiles, "runtime"),
	))

	result := executor.Execute(plan, true)

	if len(actuator.calls) != 0 {
		t.Fatalf("a dry run called the actuator: %v", actuator.calls)
	}
	if !result.DryRun {
		t.Error("report does not record that it was a dry run")
	}
	if len(result.Results) != len(plan.Actions) {
		t.Errorf("dry run reported %d results for %d actions", len(result.Results), len(plan.Actions))
	}
	for _, a := range plan.Actions {
		if a.Description == "" {
			t.Error("an action has no description to show the user")
		}
	}
}

// One failing repair must not stop the others.
func TestExecuteContinuesAfterFailure(t *testing.T) {
	actuator := newFakeActuator()
	actuator.errs["start-daemon"] = fmt.Errorf("permission denied")
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	plan := PlanFrom(report(
		repairable(diagnostics.CodeDaemonNotRunning, "daemon"),
		repairable(diagnostics.CodeSessionMissing, "coder"),
	))

	result := executor.Execute(plan, false)

	if len(result.Failed()) != 1 {
		t.Errorf("failures = %+v, want exactly one", result.Failed())
	}

	joined := strings.Join(actuator.calls, " ")
	if !strings.Contains(joined, "session:coder") {
		t.Error("a later repair was skipped because an earlier one failed")
	}
}

func TestReconcileOrphanUsesTheHandoffID(t *testing.T) {
	actuator := newFakeActuator()
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	d := repairable(diagnostics.CodeOrphanDelivery, "coder")
	d.Detail = map[string]string{"id": "abc123"}

	executor.Execute(PlanFrom(report(d)), false)

	if len(actuator.calls) != 1 || actuator.calls[0] != "reconcile:coder:abc123" {
		t.Errorf("calls = %v", actuator.calls)
	}
}

func TestReconcileWithoutIDFails(t *testing.T) {
	actuator := newFakeActuator()
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	result := executor.Execute(PlanFrom(report(
		repairable(diagnostics.CodeOrphanDelivery, "coder"),
	)), false)

	if len(result.Failed()) != 1 {
		t.Error("an orphan action with no id should fail rather than guess")
	}
}

func TestCleanTempFilesReportsCount(t *testing.T) {
	actuator := newFakeActuator()
	executor := &Executor{Actuator: actuator, Log: io.Discard}

	d := repairable(diagnostics.CodeTempFiles, "runtime")
	result := executor.Execute(PlanFrom(report(d)), false)

	if len(result.Results) != 1 || !strings.Contains(result.Results[0].Note, "3 file") {
		t.Errorf("result = %+v", result.Results)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	diags := report(
		repairable(diagnostics.CodeSessionMissing, "refactorer"),
		repairable(diagnostics.CodeSessionMissing, "coder"),
		repairable(diagnostics.CodeDaemonNotRunning, "daemon"),
	)

	first := PlanFrom(diags)
	second := PlanFrom(diags)

	if len(first.Actions) != len(second.Actions) {
		t.Fatal("plans differ in size")
	}
	for i := range first.Actions {
		a, b := first.Actions[i], second.Actions[i]
		if a.Kind != b.Kind || a.Component != b.Component || a.Description != b.Description {
			t.Fatalf("plans differ at %d: %+v vs %+v", i, a, b)
		}
	}
}
