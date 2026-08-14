package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/mbannour/swarm-go/internal/diagnostics"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/lifecycle"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// newInspector wires the live components into the diagnostics/repair machinery.
func newInspector() *lifecycle.Inspector {
	mgr := newLifecycleManager()

	cfg, wtMgr := loadWorktreeContext()
	store := handoff.NewStore(wtMgr.Root, handoff.NewRoles(configuredRoles(cfg)))

	return &lifecycle.Inspector{
		Mgr:      mgr,
		Git:      lifecycle.GitInspection{Mgr: wtMgr},
		Handoffs: lifecycle.HandoffInspection{Store: store},
		Tmux:     lifecycle.TmuxInspection{Mgr: tmux.NewManager(wtMgr.Root)},
	}
}

// runDoctor inspects the swarm. It is strictly read-only.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print machine-readable diagnostics")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}

	report := newInspector().Diagnose()

	if *asJSON {
		data, err := report.JSON()
		if err != nil {
			fail(err)
		}
		fmt.Println(string(data))
		os.Exit(report.ExitCode())
	}

	printDoctorReport(report)
	os.Exit(report.ExitCode())
}

func printDoctorReport(report diagnostics.Report) {
	fmt.Println("Swarm Doctor")
	fmt.Println()
	fmt.Printf("Repository\n  %s\n\n", report.Repository)

	// Environment summary first: these are the things doctor used to cover.
	fmt.Println("Environment")
	for _, tool := range []string{"git", "tmux"} {
		if path, err := lookPath(tool); err == nil {
			fmt.Printf("  ✓ %-10s %s\n", tool, path)
		} else {
			fmt.Printf("  ✗ %-10s not found\n", tool)
		}
	}
	for _, backend := range []string{"codex", "claude"} {
		if path, err := lookPath(backend); err == nil {
			fmt.Printf("  ✓ %-10s %s\n", backend, path)
		} else {
			fmt.Printf("  ○ %-10s not installed\n", backend)
		}
	}
	fmt.Println()

	if len(report.Diagnostics) == 0 {
		fmt.Println("Diagnostics")
		fmt.Println("  ✓ no problems found")
	} else {
		fmt.Println("Diagnostics")

		// Group by component so a role's problems read together.
		byComponent := map[string][]diagnostics.Diagnostic{}
		var order []string
		for _, d := range report.Diagnostics {
			if _, seen := byComponent[d.Component]; !seen {
				order = append(order, d.Component)
			}
			byComponent[d.Component] = append(byComponent[d.Component], d)
		}
		sort.Strings(order)

		for _, component := range order {
			fmt.Printf("\n  %s\n", component)
			for _, d := range byComponent[component] {
				fmt.Printf("  %s %-28s %s\n", marker(d.Severity), d.Code, d.Message)
				if d.Repairable {
					fmt.Printf("    → repairable with `swarm repair`\n")
				}
			}
		}
	}

	fmt.Println()
	fmt.Println("Overall")
	fmt.Printf("  %s\n", report.Health)

	switch report.Health {
	case diagnostics.HealthDegraded:
		fmt.Println("\nRun `swarm repair --dry-run` to see what would be fixed.")
	case diagnostics.HealthBlocked:
		fmt.Println("\nSome issues need you: they are marked without a repair hint above.")
		fmt.Println("Nothing was changed. See docs/recovery.md.")
	}
}

func marker(s diagnostics.Severity) string {
	switch s {
	case diagnostics.SeverityCritical, diagnostics.SeverityError:
		return "✗"
	case diagnostics.SeverityWarning:
		return "⚠"
	default:
		return "○"
	}
}

// runRepair diagnoses, plans and applies safe repairs.
func runRepair(args []string) {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be repaired, change nothing")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}

	inspector := newInspector()

	plan, report, err := inspector.Repair(*dryRun)
	if err != nil {
		fail(err)
	}

	fmt.Println("Swarm Repair")
	fmt.Println()

	if plan.Empty() && len(plan.Blocked) == 0 {
		fmt.Println("Nothing to repair.")
		fmt.Println()
		fmt.Println("Result: HEALTHY")
		return
	}

	if *dryRun {
		if plan.Empty() {
			fmt.Println("Nothing would be repaired automatically.")
		} else {
			fmt.Println("Would repair:")
			fmt.Println()
			for _, a := range plan.Actions {
				fmt.Printf("  - %s\n", a.Description)
			}
		}
	} else {
		for _, res := range report.Results {
			switch {
			case res.Err != nil:
				fmt.Printf("✗ %s: %v\n", res.Action.Description, res.Err)
			case res.Note != "":
				fmt.Printf("✓ %s (%s)\n", res.Action.Description, res.Note)
			default:
				fmt.Printf("✓ %s\n", res.Action.Description)
			}
		}
	}

	if len(plan.Blocked) > 0 {
		fmt.Println()
		fmt.Println("Needs you (not repaired):")
		fmt.Println()
		for _, d := range plan.Blocked {
			fmt.Printf("  ! %-12s %-28s %s\n", d.Component, d.Code, d.Message)
		}
	}

	fmt.Println()

	if *dryRun {
		fmt.Println("No changes made.")
		return
	}

	// Re-diagnose so the result reflects reality rather than intent.
	after := inspector.Diagnose()
	fmt.Printf("Result: %s\n", upper(string(after.Health)))

	if len(report.Failed()) > 0 || after.Health == diagnostics.HealthBlocked {
		os.Exit(1)
	}
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// lookPath is a small indirection so doctor's environment block stays testable.
var lookPath = func(name string) (string, error) {
	return execLookPath(name)
}

// handoffRetry moves a failed handoff back into its sender's outbox, but only
// when the failure was transient. A malformed or invalid message is permanent
// and stays where it is.
func handoffRetry(store *handoff.Store, repoRoot string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: swarm handoff retry <role> <handoff-id>")
	}
	role, id := args[0], args[1]

	entries, err := store.List(role, handoff.BoxFailed)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.ID != id {
			continue
		}

		// Re-check the message itself. If it is still invalid, retrying would
		// only fail again in the same way.
		if err := handoff.Validate(e.Handoff, store.Roles, role); err != nil {
			return fmt.Errorf("handoff %s is permanently invalid, not retryable: %w", id, err)
		}
		if e.Type == handoff.TypeGit {
			if _, err := git.NewRepo(repoRoot).ResolveCommit(e.Commit); err != nil {
				return fmt.Errorf("handoff %s names an unresolvable commit, not retryable: %w", id, err)
			}
		}

		dst, err := store.MoveTo(e.Path, role, handoff.BoxOutbox)
		if err != nil {
			return err
		}

		fmt.Println("RETRY_QUEUED")
		fmt.Printf("ID: %s\n", id)
		fmt.Printf("FILE: %s\n", dst)
		fmt.Println("The daemon will attempt delivery on its next pass.")

		return nil
	}

	return fmt.Errorf("no failed handoff %q for role %q", id, role)
}

// execLookPath is exec.LookPath, wrapped so doctor.go owns the import.
func execLookPath(name string) (string, error) { return exec.LookPath(name) }
