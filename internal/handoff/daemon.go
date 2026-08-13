package handoff

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// DefaultInterval is how often the daemon scans the outboxes.
const DefaultInterval = 250 * time.Millisecond

// Daemon scans configured outboxes and delivers valid messages to inboxes.
// One bad file never stops the loop: it is quarantined and the scan continues.
type Daemon struct {
	Store    *Store
	Roles    []string // configured role names, in configuration order
	Notifier Notifier // may be nil: delivery still happens, nobody is woken
	Interval time.Duration
	Log      io.Writer
}

// NewDaemon returns a daemon with sane defaults.
func NewDaemon(store *Store, roles []string, n Notifier) *Daemon {
	return &Daemon{
		Store:    store,
		Roles:    roles,
		Notifier: n,
		Interval: DefaultInterval,
		Log:      os.Stdout,
	}
}

// ScanResult summarises one pass over every outbox.
type ScanResult struct {
	Delivered int
	Rejected  int
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

	// Roles whose agent should be woken once at the end of the pass.
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

		// Unparsable files: quarantine, keep going.
		for _, name := range bad {
			d.reject(filepath.Join(dir, name), role, "file is not a valid handoff")
			result.Rejected++
		}

		// list() already ordered by priority descending, then timestamp.
		for _, e := range entries {
			if err := Validate(e.Handoff, d.Store.Roles, role); err != nil {
				d.reject(e.Path, role, err.Error())
				result.Rejected++
				continue
			}

			dst, err := d.Store.Deliver(e)
			if err != nil {
				d.logf("deliver %s: %v", e.Name, err)
				continue
			}

			d.logf("delivered %s -> %s (%s, priority %d)", e.From, e.To, e.Type, e.Priority)
			_ = dst

			result.Delivered++
			woken[e.To] = true
		}
	}

	for role := range woken {
		d.notify(role)
	}

	return result
}

// reject quarantines a file, logging both outcomes.
func (d *Daemon) reject(path, role, reason string) {
	dst, err := d.Store.Reject(path, reason)
	if err != nil {
		d.logf("cannot quarantine %s: %v", filepath.Base(path), err)
		return
	}
	d.logf("rejected %s/%s: %s", role, filepath.Base(path), reason)
	_ = dst
}

// notify wakes a destination agent with fixed text. Failure is not fatal: the
// message is already durable in the inbox.
func (d *Daemon) notify(role string) {
	if d.Notifier == nil {
		return
	}
	if err := d.Notifier.Notify(role); err != nil {
		d.logf("notify %s: %v", role, err)
	}
}

func (d *Daemon) logf(format string, args ...interface{}) {
	if d.Log == nil {
		return
	}
	fmt.Fprintf(d.Log, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
