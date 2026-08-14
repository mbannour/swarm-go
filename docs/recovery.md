# Recovery

Things break: a laptop sleeps, a process is killed, a terminal is closed
mid-operation. This is how to get a swarm back without hand-editing `.swarm/`.

Three commands cover almost everything:

```bash
swarm doctor            # what is wrong? (changes nothing)
swarm repair --dry-run  # what would be fixed?
swarm repair            # fix the safe ones
```

## The rules repair follows

1. **It never destroys work.** Uncommitted changes, failed handoffs, rejected
   handoffs, completed handoffs and daemon logs are always preserved.
2. **It never guesses.** Anything ambiguous is reported for you to resolve, not
   repaired.
3. **It only touches managed state** — `.swarm/`, this project's tmux socket,
   this project's worktrees. Never anything else on your machine.
4. **It never signals an unverified process.** A pid is only acted on when the
   lock, the recorded repository and a live process all agree.

## Health and exit codes

| Health | Meaning | `swarm doctor` exit code |
|---|---|---|
| `healthy` | everything required is working | 0 |
| `stopped` | intentionally not running | 0 |
| `degraded` | recoverable; try `swarm repair` | 1 |
| `blocked` | unsafe or ambiguous; needs you | 2 |

`swarm repair` exits non-zero if a repair failed or blocking issues remain.

## Common cases

### The handoff daemon crashed

Symptom: handoffs stop moving; agents sit idle with full inboxes.

```
$ swarm doctor
  daemon
  ✗ DAEMON_NOT_RUNNING   the handoff daemon is not running; handoffs will not be delivered
  ⚠ DAEMON_STALE_PID     daemon metadata names a process that is not running
```

```bash
swarm repair
```

Nothing is lost: handoffs are files, and anything waiting in an outbox is
delivered on the daemon's next pass.

### An agent exited but its session is alive

Symptom: you attach to a role and see a shell prompt instead of the agent.

```
  coder
  ✗ AGENT_MISSING   the session is running but its agent has exited
```

`swarm repair` restarts the backend in the existing session with its normal
prompt. The role's current work is untouched, so it resumes with
`swarm handoff ready <role>`.

### A tmux session disappeared

```
  refactorer
  ✗ SESSION_MISSING   tmux session is missing while the rest of the swarm is running
```

`swarm repair` recreates that one session in its worktree and starts its agent.
Healthy roles are not restarted.

### A stale tmux socket

A socket file left behind by a dead server. Swarm never treats the file's
existence as proof of a server — it asks tmux.

```
  tmux
  ⚠ TMUX_SOCKET_STALE   the tmux socket file exists but no server answers through it
```

`swarm repair` removes it, but only after confirming nothing answers, and only
this project's socket.

### Stale daemon PID metadata

```
  daemon
  ⚠ DAEMON_STALE_PID   daemon metadata names a process that is not running
```

Cleared by `swarm repair`. A pid file is never trusted on its own: pids get
recycled, so swarm refuses to signal a process it cannot verify. If you see
`DAEMON_UNVERIFIED` instead, some other process holds the daemon lock — find and
stop it yourself.

### A dirty worktree

```
  coder
  ⚠ WORKTREE_DIRTY   worktree …/wt-coder has uncommitted changes; commit or stash
                     them yourself — swarm will not touch them
```

**This is never repaired automatically**, by design. Repair has no way to know
whether those changes matter. Deal with them in the worktree:

```bash
cd .swarm/worktrees/wt-coder
git status
git add -A && git commit -m "…"   # or: git stash
```

### A missing worktree

If the directory is gone but Git still has it registered, the situation is
ambiguous and repair leaves it alone. Clear the stale registration first, then
repair:

```bash
git worktree prune      # or: swarm repair, once nothing is registered
swarm repair
```

Swarm only ever prunes registrations under `.swarm/worktrees`, and refuses to
prune at all if an unrelated worktree is also prunable.

### An integration conflict

```
  architect
  ✗ INTEGRATION_FAILED   integrating the handed-off commit failed: cherry-pick
                         conflict applying f638d3d5f2: impl.txt
```

The cherry-pick was aborted, so the worktree is clean and unchanged — but the
change was not applied. Repair will not resolve it. Either fix it by hand in the
role's worktree, or send a note upstream describing the conflict:

```bash
cd .swarm/worktrees/wt-architect
git cherry-pick <source-commit>     # resolve, then: git cherry-pick --continue
swarm handoff integrate architect   # confirms it is now already-integrated
```

`INTEGRATION_PENDING` is the harmless sibling: the commit simply has not been
applied yet. `swarm repair` will run the integration for you.

### A failed handoff

Failed means the message was valid but could not be delivered — most often a
commit that did not resolve.

```
  coder
  ⚠ HANDOFF_FAILED   1 handoff(s) failed to send; see `swarm handoff retry`
```

Failures are not retried automatically. Look at the reason, then decide:

```bash
cat .swarm/handoffs/coder/failed/*.reason
swarm handoff retry coder <handoff-id>
```

`retry` re-validates first and refuses permanently broken messages — an unknown
role, a malformed file, or a commit that still does not exist.

### An orphaned delivery

A crash between delivering a handoff and recording that it was sent:

```
  coder
  ⚠ HANDOFF_ORPHAN_DELIVERY   handoff 3c4756db was already delivered but is still
                              queued in the coder outbox
```

`swarm repair` reconciles the sender's bookkeeping. It does **not** redeliver:
the destination keeps exactly one copy.

### Current work after a restart

Nothing to do. `current/` is on disk, so after `swarm start` a role's
`handoff ready` returns the same task it was already working on. If the process
died between `handoff next` and `handoff done`, check:

```bash
swarm handoff status coder
```

`DOWNSTREAM_SENT: yes` means the handoff already exists — run `handoff done`.
Re-running `handoff next` is safe anyway; it returns the original.

### Two current tasks in task mode

```
  coder
  ✗ HANDOFF_CURRENT_CORRUPT   2 items are in current/ but coder receives one task
                              at a time; move the extras back to inbox/ yourself
```

Blocked on purpose: picking a winner automatically could strand real work. Look
at both files and move the one you are not doing back:

```bash
ls .swarm/handoffs/coder/current/
mv .swarm/handoffs/coder/current/<the-other-one> .swarm/handoffs/coder/inbox/
```

### Leftover temporary files

`.tmp-*` files inside `.swarm/` older than ten minutes are the remains of an
interrupted atomic write. `swarm repair` removes them — only inside `.swarm/`,
and only that pattern.

## Diagnostic codes

Automation should match these, not the messages.

| Code | Repairable | Meaning |
|---|---|---|
| `REPO_MISSING` | no | not inside a Git repository |
| `REPO_NO_COMMITS` | no | no commit to branch worktrees from |
| `CONFIG_INVALID` | no | `swarm.conf` is wrong |
| `PROMPTS_MISSING` | no | a prompt file is absent |
| `BACKEND_MISSING` | no | the configured agent CLI is not installed |
| `RUNTIME_NOT_WRITABLE` | no | `.swarm/runtime` cannot be written |
| `WORKTREE_MISSING` | if unregistered | worktree directory is gone |
| `WORKTREE_DIRTY` | **no** | uncommitted changes; never touched |
| `WORKTREE_WRONG_BRANCH` | no | checked out somewhere unexpected |
| `WORKTREE_DETACHED` | no | detached HEAD |
| `WORKTREE_INVALID_METADATA` | no | Git metadata is inconsistent |
| `TMUX_MISSING` | no | tmux is not installed |
| `TMUX_SOCKET_STALE` | yes | socket file with no server behind it |
| `SESSION_MISSING` | yes | a role's session is gone |
| `AGENT_MISSING` | yes | session alive, agent exited |
| `DAEMON_NOT_RUNNING` | yes | no handoff daemon |
| `DAEMON_STALE_PID` | yes | pid metadata for a dead process |
| `DAEMON_UNVERIFIED` | **no** | something holds the lock that swarm cannot identify |
| `HANDOFF_FAILED` | no | delivery failed; retry explicitly |
| `HANDOFF_REJECTED` | no | invalid messages quarantined |
| `HANDOFF_CURRENT_CORRUPT` | **no** | more current work than the mode allows |
| `HANDOFF_ORPHAN_DELIVERY` | yes | delivered but not recorded as sent |
| `INTEGRATION_PENDING` | yes | a handed-off commit has not been applied yet |
| `INTEGRATION_FAILED` | **no** | cherry-pick conflict; needs a human |
| `RUNTIME_TEMP_FILES` | yes | abandoned atomic-write temporaries |
| `LOCK_ACTIVE` | n/a | a lifecycle operation is running elsewhere |

## Concurrency

`repair` takes the same lifecycle lock as `start` and `stop`, so it cannot race
with them or with another `repair`. If one is already running you will see:

```
another swarm lifecycle operation is running for this repository
```

Wait for it and retry.

## When repair is not enough

Everything durable is a file, so manual recovery is always possible:

```
.swarm/handoffs/<role>/{inbox,outbox,sent,failed,current,completed}/
.swarm/handoffs/{rejected,archive}/
.swarm/runtime/logs/handoffd.log
```

Read `swarm logs daemon` first — a delivery that never happened usually has its
reason written down. Moving a `.handoff` file between boxes by hand is a
legitimate last resort; the daemon and lifecycle will pick it up from wherever
you put it.
