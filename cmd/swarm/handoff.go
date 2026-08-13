package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mbannour/swarm-go/internal/config"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

func runHandoff(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: swarm handoff <send|inbox|outbox|ack|daemon>")
		os.Exit(1)
	}

	cfg, wtMgr := loadWorktreeContext()
	roleNames := configuredRoles(cfg)
	store := handoff.NewStore(wtMgr.Root, handoff.NewRoles(roleNames))

	if err := store.EnsureDirs(roleNames); err != nil {
		fail(err)
	}

	switch args[0] {
	case "send":
		fail(handoffSend(store, args[1:]))
	case "inbox":
		fail(handoffBox(store, args[1:], true))
	case "outbox":
		fail(handoffBox(store, args[1:], false))
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

func handoffSend(store *handoff.Store, args []string) error {
	fs := flag.NewFlagSet("handoff send", flag.ContinueOnError)

	var (
		from     = fs.String("from", "", "sending role")
		to       = fs.String("to", "", "destination role")
		typ      = fs.String("type", string(handoff.TypeNote), "git_handoff or note")
		task     = fs.String("task", "", "task identifier (required for git_handoff)")
		commit   = fs.String("commit", "", "commit object name (required for git_handoff)")
		priority = fs.Int("priority", 10, "0..100, higher is more urgent")
		note     = fs.String("note", "", "human-readable message")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	h := handoff.Handoff{
		Type:     handoff.Type(*typ),
		From:     *from,
		To:       *to,
		Task:     *task,
		Commit:   *commit,
		Priority: *priority,
		Note:     *note,
	}

	path, err := store.Send(h)
	if err != nil {
		return fmt.Errorf("send handoff: %w", err)
	}

	fmt.Printf("✓ queued %s -> %s in %s outbox\n", h.From, h.To, h.From)
	fmt.Printf("  %s\n", path)

	return nil
}

func handoffBox(store *handoff.Store, args []string, inbox bool) error {
	label := "OUTBOX"
	if inbox {
		label = "INBOX"
	}

	if len(args) == 0 {
		return fmt.Errorf("usage: swarm handoff %s <role>", lower(label))
	}
	role := args[0]

	var (
		entries []handoff.Entry
		err     error
	)
	if inbox {
		entries, err = store.Inbox(role)
	} else {
		entries, err = store.Outbox(role)
	}
	if err != nil {
		return err
	}

	fmt.Printf("%s: %s\n\n", label, role)

	if len(entries) == 0 {
		fmt.Println("(empty)")
		return nil
	}

	peer := "FROM"
	if !inbox {
		peer = "TO"
	}
	fmt.Printf("%-9s %-11s %-13s %-10s %s\n", "PRIORITY", peer, "TYPE", "TASK", "FILE")

	for _, e := range entries {
		other := e.From
		if !inbox {
			other = e.To
		}
		fmt.Printf("%-9d %-11s %-13s %-10s %s\n", e.Priority, other, e.Type, dash(e.Task), e.Name)
	}

	return nil
}

func handoffAck(store *handoff.Store, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: swarm handoff ack <role> <filename>")
	}

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

	d := handoff.NewDaemon(store, roles, notifier)
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

func lower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}
