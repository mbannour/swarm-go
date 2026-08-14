package handoff

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultInterval is how often the daemon scans the outboxes.
const DefaultInterval = 250 * time.Millisecond

// deliveryAttempts is how often a transient filesystem failure is retried
// within one scan. Permanent faults are never retried.
const deliveryAttempts = 3

// CommitResolver verifies that a commit abbreviation names exactly one commit
// and returns its canonical SHA. The daemon resolves against the project
// repository only — never a path taken from a message.
type CommitResolver interface {
	ResolveCommit(abbrev string) (string, error)
}

// Daemon scans configured outboxes and delivers messages to inboxes.
// One bad file never stops the loop.
type Daemon struct {
	Store    *Store
	Roles    []string       // configured role names, in configuration order
	Notifier Notifier       // may be nil: delivery still happens, nobody is woken
	Commits  CommitResolver // may be nil: git_handoff commits are then unverifiable
	Interval time.Duration
	Log      io.Writer
}

// NewDaemon returns a daemon with sane defaults.
func NewDaemon(store *Store, roles []string, n Notifier, commits CommitResolver) *Daemon {
	return &Daemon{
		Store:    store,
		Roles:    roles,
		Notifier: n,
		Commits:  commits,
		Interval: DefaultInterval,
		Log:      os.Stdout,
	}
}

// ScanResult summarises one pass over every outbox.
type ScanResult struct {
	Delivered int // destination copies written
	Duplicate int // destination copies already present
	Sent      int // logical handoffs fully delivered
	Rejected  int // invalid messages quarantined
	Failed    int // valid requests that could not be completed
}

// Run scans until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Store.EnsureDirs(d.Roles); err != nil {
		return err
	}

	interval := d.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	d.logf("handoff daemon watching %d roles every %s", len(d.Roles), interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.Scan()
		case <-ctx.Done():
			d.logf("handoff daemon stopped")
			return nil
		}
	}
}

// Scan makes one pass over every configured outbox. Errors are logged rather
// than returned so a single unreadable file cannot take the daemon down.
func (d *Daemon) Scan() ScanResult {
	var result ScanResult

	woken := map[string]bool{}

	for _, role := range d.Roles {
		dir, err := d.Store.OutboxDir(role)
		if err != nil {
			d.logf("skip outbox for %s: %v", role, err)
			continue
		}

		entries, bad, err := d.Store.list(dir)
		if err != nil {
			d.logf("read outbox for %s: %v", role, err)
			continue
		}

		// Unparsable files are a fault of the message: quarantine, keep going.
		for _, name := range bad {
			d.reject(filepath.Join(dir, name), role, "file is not a valid handoff")
			result.Rejected++
		}

		for _, e := range entries {
			d.process(role, e, &result, woken)
		}
	}

	for role := range woken {
		d.notify(role)
	}

	return result
}

// process handles one logical handoff from a role's outbox.
func (d *Daemon) process(role string, e Entry, result *ScanResult, woken map[string]bool) {
	// 1. Is the message itself well formed? A fault here is permanent.
	if err := Validate(e.Handoff, d.Store.Roles, role); err != nil {
		d.reject(e.Path, role, err.Error())
		result.Rejected++
		return
	}

	// 2. Does the commit exist? A valid request naming a commit we cannot
	//    resolve is a failed request, not a malformed message, so it lands in
	//    the sender's failed/ box where the sender can see and re-send it.
	h := e.Handoff
	if h.Type == TypeGit {
		if d.Commits == nil {
			d.fail(role, e.Path, "no commit resolver configured; cannot verify git_handoff")
			result.Failed++
			return
		}

		canonical, err := d.Commits.ResolveCommit(h.Commit)
		if err != nil {
			d.fail(role, e.Path, err.Error())
			result.Failed++
			return
		}
		h.CanonicalCommit = canonical
	}

	// 3. Deliver to each destination independently. One destination failing
	//    must never undo or block another.
	var failures []string
	delivered := 0

	for _, to := range h.To {
		path, already, err := d.deliver(h, to)
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", to, err))
			d.logf("deliver %s -> %s failed: %v", h.From, to, err)
		case already:
			result.Duplicate++
			delivered++
			d.logf("already delivered %s -> %s (id %s)", h.From, to, short(h.ID))
		default:
			result.Delivered++
			delivered++
			woken[to] = true
			d.logf("delivered %s -> %s (%s, priority %d, id %s)", h.From, to, h.Type, h.Priority, short(h.ID))
			_ = path
		}
	}

	// 4. Retire the source. Fully delivered goes to sent/; anything else goes
	//    to failed/ with the reason — the successful copies stay delivered.
	if len(failures) == 0 {
		if _, err := d.Store.MoveTo(e.Path, role, BoxSent); err != nil {
			d.logf("archive sent %s: %v", e.Name, err)
			return
		}
		result.Sent++
		return
	}

	reason := fmt.Sprintf(
		"delivered to %d of %d destinations; failures: %s",
		delivered, len(h.To), strings.Join(failures, "; "),
	)
	d.fail(role, e.Path, reason)
	result.Failed++
}

// deliver writes one destination copy, retrying transient filesystem errors.
func (d *Daemon) deliver(h Handoff, to string) (path string, already bool, err error) {
	for attempt := 1; ; attempt++ {
		path, already, err = d.Store.Deliver(h, to)
		if err == nil || attempt >= deliveryAttempts {
			return path, already, err
		}
		d.logf("deliver %s -> %s attempt %d failed: %v", h.From, to, attempt, err)
		time.Sleep(time.Duration(attempt) * 20 * time.Millisecond)
	}
}

// reject quarantines an invalid message.
func (d *Daemon) reject(path, role, reason string) {
	if _, err := d.Store.Reject(path, reason); err != nil {
		d.logf("cannot quarantine %s: %v", filepath.Base(path), err)
		return
	}
	d.logf("rejected %s/%s: %s", role, filepath.Base(path), reason)
}

// fail records a valid request that could not be completed.
func (d *Daemon) fail(role, path, reason string) {
	if _, err := d.Store.Fail(role, path, reason); err != nil {
		d.logf("cannot record failure for %s: %v", filepath.Base(path), err)
		return
	}
	d.logf("failed %s/%s: %s", role, filepath.Base(path), reason)
}

// notify wakes a destination agent with fixed text.
//
// Notification is best effort by design: the message is already durable in the
// inbox, so a failed wake-up is logged and never rolls back a delivery. The
// recipient finds the work with `swarm handoff ready <role>`.
func (d *Daemon) notify(role string) {
	if d.Notifier == nil {
		return
	}
	if err := d.Notifier.Notify(role); err != nil {
		d.logf("delivered successfully; notification to %s failed: %v", role, err)
	}
}

func (d *Daemon) logf(format string, args ...interface{}) {
	if d.Log == nil {
		return
	}
	fmt.Fprintf(d.Log, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
