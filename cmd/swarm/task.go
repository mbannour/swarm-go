package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// EntryRole is where externally submitted work enters the four-pack.
const EntryRole = "specifier"

func runTask(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm task <submit|trace>")
		os.Exit(1)
	}

	cfg, wtMgr := loadWorktreeContext()
	roleNames := configuredRoles(cfg)
	store := handoff.NewStore(wtMgr.Root, handoff.NewRoles(roleNames))

	if err := store.EnsureDirs(roleNames); err != nil {
		fail(err)
	}

	switch args[0] {
	case "submit":
		fail(taskSubmit(store, wtMgr.Root, args[1:]))
	case "trace":
		fail(taskTrace(store, args[1:]))
	default:
		fmt.Printf("unknown task subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// taskSubmit is the developer boundary: work entering the swarm from outside.
//
// The submitter is not a role — it owns no worktree, session or outbox — so
// this does not weaken the rule that only configured roles exchange handoffs.
func taskSubmit(store *handoff.Store, repoRoot string, args []string) error {
	fs := flag.NewFlagSet("task submit", flag.ContinueOnError)

	var (
		id          = fs.String("id", "", "task identifier, e.g. DEMO-1")
		description = fs.String("description", "", "what needs to be done")
		priority    = fs.Int("priority", 20, "0..100, higher is more urgent")
		to          = fs.String("to", EntryRole, "entry role")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *description == "" {
		return fmt.Errorf("usage: swarm task submit --id <id> --description <text> [--priority n]")
	}

	// The requirement is a note: it carries no commit, because nothing has been
	// built yet. The task id travels in the note so the whole cycle is
	// traceable from the first message.
	entry, err := store.Submit(handoff.Handoff{
		Type:     handoff.TypeNote,
		Priority: *priority,
		Note:     fmt.Sprintf("[%s] %s", *id, *description),
	}, *to)
	if err != nil {
		return fmt.Errorf("submit task: %w", err)
	}

	fmt.Printf("TASK_SUBMITTED: %s\n", *id)
	fmt.Printf("DESTINATION: %s\n", *to)
	fmt.Printf("ID: %s\n", entry.ID)
	fmt.Printf("FILE: %s\n", entry.Path)

	// A submitted task lands straight in the inbox, so the daemon — which only
	// notifies on delivery from an outbox — never sees it. Without this the
	// entry role sits idle with work waiting and nothing telling it to look.
	if err := wakeRole(repoRoot, *to, entry.ID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not wake %s: %v\n", *to, err)
		fmt.Println("NOTIFIED: no")
		return nil
	}

	fmt.Println("NOTIFIED: yes")

	return nil
}

// wakeRole tells a role's agent to check its inbox, through the same tracked
// path the daemon uses — so a submitted task is notified, recorded and
// reconciled exactly like a delivered one.
//
// A role with no running session is not an error: the work is durable and
// `handoff ready` finds it whenever the agent starts.
func wakeRole(repoRoot, role, handoffID string) error {
	if !tmux.Available() {
		return nil
	}

	mgr := tmux.NewManager(repoRoot)

	live, err := mgr.HasSession(tmux.SessionName(role))
	if err != nil || !live {
		return err
	}

	return newWaker(repoRoot, false).Notify(role, handoffID)
}

// taskTrace reconstructs a task's journey from durable handoff metadata.
func taskTrace(store *handoff.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm task trace <task-id>")
	}
	task := args[0]

	events, err := store.Trace(task)
	if err != nil {
		return err
	}

	fmt.Printf("TASK %s\n\n", task)

	if len(events) == 0 {
		fmt.Println("NO_EVENTS")
		return nil
	}

	for _, e := range events {
		from := e.From
		if from == handoff.SystemSender {
			from = "developer"
		}

		fmt.Printf("%s -> %s\n", from, e.Owner)
		fmt.Printf("  ID: %s\n", e.ID)
		if e.SourceID != "" {
			fmt.Printf("  SOURCE_ID: %s\n", e.SourceID)
		}
		fmt.Printf("  TYPE: %s\n", e.Type)
		fmt.Printf("  STATE: %s\n", e.Box)
		if e.CanonicalCommit != "" {
			fmt.Printf("  SOURCE_COMMIT: %s\n", e.CanonicalCommit)
		}
		if e.IntegrationStatus != "" {
			fmt.Printf("  INTEGRATION: %s\n", e.IntegrationStatus)
		}
		if e.IntegrationMethod != "" {
			fmt.Printf("  METHOD: %s\n", e.IntegrationMethod)
		}
		// A cherry-pick rewrites the commit; never hide that.
		if e.LocalCommit != "" && e.LocalCommit != e.CanonicalCommit {
			fmt.Printf("  LOCAL_COMMIT: %s\n", e.LocalCommit)
		}
		if e.Note != "" {
			fmt.Printf("  NOTE: %s\n", firstLine(e.Note))
		}
		fmt.Println()
	}

	fmt.Printf("EVENTS: %d\n", len(events))

	return nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
