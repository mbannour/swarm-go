package lifecycle

import "context"

// Status observes the swarm without changing anything.
//
// It is strictly read-only: it starts nothing, stops nothing, repairs nothing,
// and creates no directories. Everything it reports is observed live — tmux is
// asked about sessions, the operating system about the daemon, the filesystem
// about work — so persisted metadata is never treated as truth.
func (m *Manager) Status(ctx context.Context) (SwarmStatus, error) {
	status := SwarmStatus{Repository: m.RepoRoot}

	if meta, ok := readMetadata(m.RepoRoot); ok && !meta.StartedAt.IsZero() {
		status.StartedAt = meta.StartedAt.Format("2006-01-02T15:04:05Z07:00")
	}

	daemonState, pid, err := DaemonState(m.RepoRoot)
	if err != nil {
		return SwarmStatus{}, err
	}
	status.Daemon = DaemonStatus{State: daemonState, PID: pid, Log: DaemonLogPath(m.RepoRoot)}

	for _, r := range m.Roles {
		if ctx.Err() != nil {
			return SwarmStatus{}, ctx.Err()
		}

		roleStatus := RoleStatus{Role: r.Name, Work: "unknown"}

		present, err := m.Worktrees.Present(r)
		switch {
		case err != nil:
			roleStatus.Worktree = StateUnknown
		case present:
			roleStatus.Worktree = StateRunning
		default:
			roleStatus.Worktree = StateMissing
		}

		live, err := m.Sessions.Present(r)
		switch {
		case err != nil:
			roleStatus.Session = StateUnknown
		case live:
			roleStatus.Session = StateRunning
		default:
			roleStatus.Session = StateMissing
		}

		agentState, err := m.Agents.State(r)
		if err != nil {
			roleStatus.Agent = StateUnknown
		} else {
			roleStatus.Agent = mapAgentState(agentState)
		}

		if m.Work != nil {
			work, task, err := m.Work.Work(r.Name)
			if err == nil {
				roleStatus.Work = work
				roleStatus.Task = task
			}

			status, attempts, lastErr := m.Work.Notification(r.Name)
			roleStatus.Notification = NotificationStatus{
				Status: status, Attempts: attempts, Error: lastErr,
			}
		}

		status.Roles = append(status.Roles, roleStatus)
	}

	if m.Work != nil {
		counts, err := m.Work.Counts()
		if err == nil {
			status.Handoffs = counts
		}
	}

	status.Health = health(status)

	return status, nil
}

// mapAgentState translates the agent package's vocabulary into component state.
func mapAgentState(state string) ComponentState {
	switch state {
	case "running":
		return StateRunning
	case "not-started":
		return StateStopped
	case "session-missing":
		return StateMissing
	case "backend-missing":
		return StateFailed
	default:
		return StateUnknown
	}
}

// health condenses the picture into one verdict:
//
//	stopped  — nothing is running
//	healthy  — daemon up, and every role has a session with a live agent
//	degraded — partially up; a repeat `swarm start` should repair it
//	failed   — something is in a state that needs a human
func health(s SwarmStatus) Health {
	if !s.Running() {
		if s.Daemon.State == StateFailed {
			return HealthFailed
		}
		return HealthStopped
	}

	if s.Daemon.State == StateFailed {
		return HealthFailed
	}

	healthy := s.Daemon.State == StateRunning

	for _, r := range s.Roles {
		if r.Agent == StateFailed {
			return HealthFailed
		}
		if r.Worktree != StateRunning || r.Session != StateRunning || r.Agent != StateRunning {
			healthy = false
		}
	}

	if healthy {
		return HealthHealthy
	}

	return HealthDegraded
}
