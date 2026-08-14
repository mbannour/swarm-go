package lifecycle

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mbannour/swarm-go/internal/diagnostics"
	"github.com/mbannour/swarm-go/internal/git"
	"github.com/mbannour/swarm-go/internal/handoff"
	"github.com/mbannour/swarm-go/internal/tmux"
)

// hasCommits reports whether the repository has anything to branch from.
func hasCommits(root string) bool { return git.HasCommits(root) }

// GitInspection answers worktree questions with Git itself.
type GitInspection struct {
	Mgr *git.WorktreeManager
}

// Inspect classifies one role's worktree.
func (g GitInspection) Inspect(role, worktreeName, branch, path string) (diagnostics.Worktree, error) {
	out := diagnostics.Worktree{Path: path, ExpectedBranch: branch}

	wt, err := g.Mgr.Plan(role, worktreeName)
	if err != nil {
		return out, err
	}

	registered, err := g.Mgr.Exists(wt)
	if err != nil {
		return out, err
	}
	out.Registered = registered

	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		out.State = diagnostics.WorktreeMissing
		return out, nil
	}

	// Present on disk: ask Git what it thinks of it.
	head, err := g.Mgr.WorktreeBranch(path)
	if err != nil {
		out.State = diagnostics.WorktreeInvalid
		return out, nil
	}
	out.ActualBranch = head

	switch {
	case head == "HEAD" || head == "":
		out.State = diagnostics.WorktreeDetached
		return out, nil
	case head != branch:
		out.State = diagnostics.WorktreeWrongRef
		return out, nil
	}

	dirty, err := g.Mgr.WorktreeDirty(path)
	if err != nil {
		out.State = diagnostics.WorktreeInvalid
		return out, nil
	}
	if dirty {
		out.State = diagnostics.WorktreeDirty
		return out, nil
	}

	out.State = diagnostics.WorktreeHealthy

	return out, nil
}

// StaleMetadata reports registrations Git would prune, limited to managed paths.
func (g GitInspection) StaleMetadata() ([]string, error) {
	return g.Mgr.PrunableManaged()
}

// Prune removes stale registrations. The manager restricts this to worktrees
// under .swarm/worktrees, so an unrelated external worktree is never pruned.
func (g GitInspection) Prune() error { return g.Mgr.PruneManaged() }

// HandoffInspection answers durable-state questions.
type HandoffInspection struct {
	Store *handoff.Store
}

func (h HandoffInspection) CurrentCount(role string) (int, error) {
	entries, err := h.Store.List(role, handoff.BoxCurrent)
	return len(entries), err
}

func (h HandoffInspection) FailedCount(role string) (int, error) {
	entries, err := h.Store.List(role, handoff.BoxFailed)
	return len(entries), err
}

func (h HandoffInspection) RejectedCount() (int, error) {
	entries, _, err := h.Store.ListDir(h.Store.RejectedDir())
	return len(entries), err
}

func (h HandoffInspection) PendingOutbox(role string) (int, error) {
	entries, err := h.Store.List(role, handoff.BoxOutbox)
	return len(entries), err
}

// Orphans finds handoffs that were delivered but never retired on the sender
// side — the signature of a crash between delivery and bookkeeping.
func (h HandoffInspection) Orphans(roles []string) ([]diagnostics.Orphan, error) {
	var out []diagnostics.Orphan

	for _, role := range roles {
		entries, err := h.Store.List(role, handoff.BoxOutbox)
		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			if len(e.To) == 0 || e.ID == "" {
				continue
			}

			// Delivered to every destination? Then the source is an orphan.
			delivered := true
			for _, to := range e.To {
				has, err := h.Store.HasDelivery(to, e.ID)
				if err != nil {
					return nil, err
				}
				if !has {
					delivered = false
					break
				}
			}

			if delivered {
				out = append(out, diagnostics.Orphan{
					Role: role, ID: e.ID, Path: e.Path, Destinations: e.To,
				})
			}
		}
	}

	return out, nil
}

// Reconcile retires an already-delivered handoff into the sender's sent box.
// It never redelivers: the destination copy already exists.
func (h HandoffInspection) Reconcile(role, id string) error {
	entries, err := h.Store.List(role, handoff.BoxOutbox)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.ID != id {
			continue
		}
		_, err := h.Store.MoveTo(e.Path, role, handoff.BoxSent)
		return err
	}

	return nil // already reconciled
}

// TmuxInspection answers questions about the project's tmux server.
type TmuxInspection struct {
	Mgr *tmux.Manager
}

func (t TmuxInspection) SocketPath() string { return t.Mgr.Socket }

// ServerAlive asks tmux, rather than trusting the socket file's existence.
func (t TmuxInspection) ServerAlive() bool { return t.Mgr.ServerAlive() }

// RemoveSocket deletes this project's socket file. Callers must have confirmed
// no server answers through it first.
func (t TmuxInspection) RemoveSocket() error {
	socket := t.Mgr.Socket

	// Refuse anything that is not the per-project socket path we generate.
	if !strings.HasSuffix(socket, ".sock") || !strings.Contains(filepath.Dir(socket), "swarm-go-") {
		return nil
	}

	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}
