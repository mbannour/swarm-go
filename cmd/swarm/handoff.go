package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

func runHandoff(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm handoff <send|next|inbox|outbox|ready|current|status|done|ack|daemon>")
		os.Exit(1)
	}

	cfg, wtMgr := loadWorktreeContext()
	roleNames := configuredRoles(cfg)
	store := handoff.NewStore(wtMgr.Root, handoff.NewRoles(roleNames))

	if err := store.EnsureDirs(roleNames); err != nil {
		fail(err)
	}

	life := handoff.NewLifecycle(store, receiveModeLookup(cfg))

	switch args[0] {
	case "send":
		fail(handoffSend(store, args[1:]))
	case "next":
		fail(handoffNext(life, args[1:]))
	case "inbox":
		fail(handoffBox(store, handoff.BoxInbox, args[1:]))
	case "outbox":
		fail(handoffBox(store, handoff.BoxOutbox, args[1:]))
	case "ready":
		fail(handoffReady(life, args[1:]))
	case "current":
		fail(handoffCurrent(life, args[1:]))
	case "status":
		fail(handoffStatus(life, args[1:]))
	case "done":
		fail(handoffDone(life, args[1:]))
	case "ack":
		fail(handoffAck(store, args[1:]))
	case "daemon":
		fail(handoffDaemon(store, roleNames, wtMgr.Root, args[1:]))
	default:
		fmt.Printf("unknown handoff subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// configuredRoles returns the role names from swarm.conf, in file order.
func configuredRoles(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Roles))
	for _, r := range cfg.Roles {
		names = append(names, r.Name)
	}
	return names
}

// receiveModeLookup resolves a role's receive mode from swarm.conf. Receiver
// behavior is configuration, never hardcoded per role.
func receiveModeLookup(cfg *config.Config) func(string) (handoff.ReceiveMode, error) {
	return func(role string) (handoff.ReceiveMode, error) {
		for _, r := range cfg.Roles {
			if r.Name == role {
				return handoff.ReceiveMode(r.ReceiveMode), nil
			}
		}
		return "", fmt.Errorf("unknown role %q", role)
	}
}

func handoffSend(store *handoff.Store, args []string) error {
	fs := flag.NewFlagSet("handoff send", flag.ContinueOnError)

	var (
		from     = fs.String("from", "", "sending role")
		to       = fs.String("to", "", "destination role, or several separated by commas")
		typ      = fs.String("type", string(handoff.TypeNote), "git_handoff or note")
		task     = fs.String("task", "", "task identifier (required for git_handoff)")
		commit   = fs.String("commit", "", "10-character commit abbreviation (required for git_handoff)")
		priority = fs.Int("priority", 10, "0..100, higher is more urgent")
		note     = fs.String("note", "", "human-readable message")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	var destinations []string
	for _, part := range strings.Split(*to, ",") {
		if part = strings.TrimSpace(part); part != "" {
			destinations = append(destinations, part)
		}
	}

	entry, err := store.Send(handoff.Handoff{
		Type:     handoff.Type(*typ),
		From:     *from,
		To:       destinations,
		Task:     *task,
		Commit:   *commit,
		Priority: *priority,
		Note:     *note,
	})
	if err != nil {
		return fmt.Errorf("send handoff: %w", err)
	}

	fmt.Printf("✓ queued %s -> %s\n", entry.From, strings.Join(entry.To, ","))
	fmt.Printf("  id   %s\n", entry.ID)
	fmt.Printf("  file %s\n", entry.Path)

	return nil
}

// handoffNext sends a role's current work downstream along the four-pack
// route. It is the command agents use, and it is safe to re-run.
func handoffNext(life *handoff.Lifecycle, args []string) error {
	fs := flag.NewFlagSet("handoff next", flag.ContinueOnError)

	var (
		from     = fs.String("from", "", "sending role")
		to       = fs.String("to", "", "override the routed destination")
		typ      = fs.String("type", string(handoff.TypeNote), "git_handoff or note")
		task     = fs.String("task", "", "task identifier (required for git_handoff)")
		commit   = fs.String("commit", "", "10-character commit abbreviation (required for git_handoff)")
		priority = fs.Int("priority", 10, "0..100, higher is more urgent")
		note     = fs.String("note", "", "human-readable message")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return fmt.Errorf("usage: swarm handoff next --from <role> --type <type> --note <text> [...]")
	}

	destinations := splitRoles(*to)
	if len(destinations) == 0 {
		routed, err := handoff.NextRole(*from)
		if err != nil {
			return err
		}
		destinations = []string{routed}
	}

	entry, already, err := life.Advance(*from, handoff.Handoff{
		Type:     handoff.Type(*typ),
		To:       destinations,
		Task:     *task,
		Commit:   *commit,
		Priority: *priority,
		Note:     *note,
	})
	if err != nil {
		return fmt.Errorf("send handoff: %w", err)
	}

	if already {
		// The same current work already produced a handoff: report it rather
		// than creating a second one.
		fmt.Println("ALREADY_SENT")
	} else {
		fmt.Println("SENT")
	}

	fmt.Printf("ID: %s\n", entry.ID)
	fmt.Printf("SOURCE_ID: %s\n", entry.SourceID)
	fmt.Printf("TO: %s\n", strings.Join(entry.To, ","))
	fmt.Printf("TYPE: %s\n", entry.Type)
	fmt.Printf("FILE: %s\n", entry.Path)

	return nil
}

// handoffCurrent prints the active work without changing any state.
func handoffCurrent(life *handoff.Lifecycle, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff current <role>")
	}

	status, err := life.Status(args[0])
	if err != nil {
		return err
	}

	if len(status.Current) == 0 {
		fmt.Println("NO_CURRENT_WORK")
		return nil
	}

	for i, e := range status.Current {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("CURRENT: %s\n", e.Path)
		printEntryFields(e)
	}

	return nil
}

// handoffStatus reports a role's work state, including whether the downstream
// handoff for its current work has already been created.
func handoffStatus(life *handoff.Lifecycle, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff status <role>")
	}

	status, err := life.Status(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("ROLE: %s\n", status.Role)
	fmt.Printf("RECEIVE_MODE: %s\n", status.Mode)
	fmt.Printf("STATE: %s\n", status.State())
	fmt.Printf("CURRENT_COUNT: %d\n", len(status.Current))
	fmt.Printf("INBOX_COUNT: %d\n", status.Inbox)

	if len(status.Current) > 0 {
		fmt.Printf("CURRENT_ID: %s\n", status.Current[0].ID)
		if status.Current[0].Task != "" {
			fmt.Printf("TASK_NAME: %s\n", status.Current[0].Task)
		}
	}

	if status.DownstreamSent {
		fmt.Println("DOWNSTREAM_SENT: yes")
		for _, e := range status.Downstream {
			fmt.Printf("DOWNSTREAM_ID: %s\n", e.ID)
			fmt.Printf("DOWNSTREAM_TO: %s\n", strings.Join(e.To, ","))
		}
	} else {
		fmt.Println("DOWNSTREAM_SENT: no")
	}

	return nil
}

// splitRoles parses a comma-separated role list.
func splitRoles(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func handoffBox(store *handoff.Store, box string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff %s <role>", box)
	}
	role := args[0]

	entries, err := store.List(role, box)
	if err != nil {
		return err
	}

	fmt.Printf("%s: %s\n\n", strings.ToUpper(box), role)

	if len(entries) == 0 {
		fmt.Println("(empty)")
		return nil
	}

	peer := "FROM"
	if box == handoff.BoxOutbox {
		peer = "TO"
	}
	fmt.Printf("%-9s %-11s %-13s %-10s %s\n", "PRIORITY", peer, "TYPE", "TASK", "FILE")

	for _, e := range entries {
		other := e.From
		if box == handoff.BoxOutbox {
			other = strings.Join(e.To, ",")
		}
		fmt.Printf("%-9d %-11s %-13s %-10s %s\n", e.Priority, other, e.Type, dash(e.Task), e.Name)
	}

	return nil
}

// handoffReady prints machine-readable lines: agents parse this.
func handoffReady(life *handoff.Lifecycle, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff ready <role>")
	}

	selection, err := life.Ready(args[0])
	if err != nil {
		return err
	}

	printSelection(selection)

	return nil
}

func handoffDone(life *handoff.Lifecycle, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff done <role>")
	}

	finished, next, err := life.Done(args[0])
	if err != nil {
		return err
	}

	if len(finished) == 0 {
		fmt.Println("NO_CURRENT_WORK")
	}
	for _, e := range finished {
		if e.Task != "" {
			fmt.Printf("DONE: %s\n", e.Task)
		} else {
			fmt.Printf("DONE: %s\n", e.ID)
		}
	}

	printSelection(next)

	return nil
}

// printSelection renders a selection as stable key: value lines.
func printSelection(s handoff.Selection) {
	if s.Empty() {
		fmt.Println("NO_TASK")
		return
	}

	if s.Mode == handoff.ModeBatch {
		fmt.Printf("BATCH: %s\n", batchLabel(s))
		fmt.Printf("PRIORITY: %d\n", s.Priority)
		for _, e := range s.Entries {
			fmt.Printf("BATCH_ITEM: %s\n", e.Path)
		}
		fmt.Println()
		for _, e := range s.Entries {
			printEntry(e)
			fmt.Println()
		}
		return
	}

	printEntry(s.Entries[0])
}

// batchLabel identifies the active batch by its first item's id.
func batchLabel(s handoff.Selection) string {
	if len(s.Entries) == 0 {
		return ""
	}
	return s.Entries[0].ID
}

func printEntry(e handoff.Entry) {
	fmt.Printf("TASK: %s\n", e.Path)
	printEntryFields(e)
}

// printEntryFields prints everything but the leading path line, which differs
// between `ready` (TASK:) and `current` (CURRENT:).
func printEntryFields(e handoff.Entry) {
	fmt.Printf("ID: %s\n", e.ID)
	fmt.Printf("TYPE: %s\n", e.Type)
	fmt.Printf("FROM: %s\n", e.From)
	fmt.Printf("PRIORITY: %d\n", e.Priority)

	if e.Task != "" {
		fmt.Printf("TASK_NAME: %s\n", e.Task)
	}
	if e.Commit != "" {
		fmt.Printf("COMMIT: %s\n", e.Commit)
	}
	if e.CanonicalCommit != "" {
		fmt.Printf("CANONICAL_COMMIT: %s\n", e.CanonicalCommit)
	}
	if !e.CreatedAt.IsZero() {
		fmt.Printf("CREATED_AT: %s\n", e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !e.DeliveredAt.IsZero() {
		fmt.Printf("DELIVERED_AT: %s\n", e.DeliveredAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if e.Note != "" {
		fmt.Printf("MESSAGE: %s\n", e.Note)
	}
}

// handoffAck is the Step 6 lifecycle, kept for compatibility.
func handoffAck(store *handoff.Store, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: swarm handoff ack <role> <filename>")
	}

	fmt.Fprintln(os.Stderr, "warning: `handoff ack` is deprecated; use `handoff ready` and `handoff done`")

	dst, err := store.Ack(args[0], args[1])
	if err != nil {
		return err
	}

	fmt.Printf("✓ archived %s\n  %s\n", args[1], dst)

	return nil
}

func handoffDaemon(store *handoff.Store, roles []string, repoRoot string, args []string) error {
	fs := flag.NewFlagSet("handoff daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", handoff.DefaultInterval, "how often to scan outboxes")
	quiet := fs.Bool("quiet", false, "suppress the wake-up notification to agents")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var notifier handoff.Notifier
	if !*quiet {
		notifier = tmuxNotifier{tmux.NewManager(repoRoot)}
	}

	// Commits resolve against the project repository only — never a path
	// taken from a handoff.
	d := handoff.NewDaemon(store, roles, notifier, git.NewRepo(repoRoot))
	d.Interval = *interval

	// Stop cleanly on Ctrl-C or SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return d.Run(ctx)
}

// tmuxNotifier wakes a role's agent through the project's tmux socket. It
// sends fixed text only — no handoff content ever reaches the terminal.
type tmuxNotifier struct {
	mgr *tmux.Manager
}

func (n tmuxNotifier) Notify(role string) error {
	session := tmux.SessionName(role)

	live, err := n.mgr.HasSession(session)
	if err != nil || !live {
		return err
	}

	if err := n.mgr.SendKeys(session, handoff.WakeUpMessage); err != nil {
		return err
	}

	return n.mgr.SendKeys(session, "Enter")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
