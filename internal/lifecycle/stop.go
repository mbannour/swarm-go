package lifecycle

import (
	"context"
	"fmt"
)

// StopReport records what one stop invocation did.
type StopReport struct {
	Steps      []Step
	Status     SwarmStatus
	AlreadyOff bool
}

// Stop takes the swarm down in the reverse of startup order:
//
//	agents → sessions → daemon
//
// Agents stop first so they are interrupted while their session still exists;
// the daemon stops last so any handoff an agent produced on its way out still
// gets delivered.
//
// Stop is not cleanup. Worktrees, branches and every durable handoff box —
// inbox, outbox, sent, failed, rejected, current, completed — are left exactly
// as they are, so `swarm start` resumes the work in progress. Removing those is
// `swarm worktrees remove`, deliberately a separate command.
func (m *Manager) Stop(ctx context.Context) (StopReport, error) {
	var report StopReport

	err := m.withLifecycleLock("stop", func() error {
		before, err := m.Status(ctx)
		if err != nil {
			return err
		}
		report.AlreadyOff = !before.Running()

		record := func(name string, changed bool, err error) {
			report.Steps = append(report.Steps, Step{Name: name, Created: changed, Err: err})
		}

		// 1. Agents: a polite interrupt through the managed session, never a
		//    broad kill against arbitrary processes.
		for _, r := range m.Roles {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			stopped, err := m.Agents.Stop(r)
			record("agent:"+r.Name, stopped, err)
		}

		// 2. Sessions.
		for _, r := range m.Roles {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			removed, err := m.Sessions.Remove(r)
			record("session:"+r.Name, removed, err)
		}

		// 3. Daemon: SIGTERM, wait for it to release its lock, escalate only
		//    against the pid we verified belongs to this repository.
		if !m.SkipDaemon {
			stopped, err := StopDaemon(m.RepoRoot)
			record("daemon", stopped, err)
		}

		after, err := m.Status(ctx)
		if err != nil {
			return err
		}
		report.Status = after

		var failures int
		for _, s := range report.Steps {
			if s.Err != nil {
				failures++
			}
		}
		if failures > 0 {
			return fmt.Errorf("swarm stop incomplete: %d component(s) did not stop cleanly", failures)
		}

		return nil
	})

	return report, err
}
