# Codex backend

What swarm runs, what it needs, and what it will not do for you.

## Requirements

| | |
|---|---|
| Executable | `codex` on `PATH` |
| Authentication | `~/.codex/auth.json` — run `codex login` once |
| Workspace trust | recorded per project in `~/.codex/config.toml` |
| Verified against | `codex --help` on the installed version |

`swarm doctor` reports all four; `swarm bootstrap` prepares the last one.

## Approval policy

Autonomy is configuration, never a default. The fifth field of a `window` line
sets it, and omitting it means `interactive`:

```text
window coder codex wt-coder task autonomous
```

| Policy | Meaning |
|---|---|
| `interactive` | The agent may stop and ask a human. Safe for supervised use; **stalls an unattended swarm** at the first command it wants to run. |
| `autonomous` | Runs what it needs inside its own worktree without asking. Intended for unattended operation. |
| `restricted` | Unattended, but read-only: it can inspect and report, not modify. |

### The exact commands

Swarm generates these, with every path shell-quoted and the prompt read from a
file so no prompt text ever passes through `tmux send-keys`:

**interactive**

```bash
codex --cd '<worktree>' "$(cat '<runtime prompt>')"
```

**autonomous**

```bash
codex --cd '<worktree>' --ask-for-approval never --sandbox workspace-write "$(cat '<runtime prompt>')"
```

**restricted**

```bash
codex --cd '<worktree>' --ask-for-approval never --sandbox read-only "$(cat '<runtime prompt>')"
```

The flags come from `codex --help`:

- `-a, --ask-for-approval <untrusted|on-request|never>`
- `-s, --sandbox <read-only|workspace-write|danger-full-access>`

`autonomous` deliberately pairs `never` with `workspace-write` rather than
`danger-full-access`: the agent stops asking permission, but its writes stay
confined by Codex's own sandbox. Swarm never generates
`--dangerously-bypass-approvals-and-sandbox`.

An unsupported policy is an error at launch, never a silent downgrade.

## Workspace trust

The first time Codex runs in a directory it asks:

```text
Do you trust the contents of this directory?
› 1. Yes, continue
  2. No, quit
```

It blocks there — before it has read the prompt. With four worktrees that is
four blocked agents, which is why an unattended `swarm start` refuses to
proceed until trust is recorded:

```
✗ backend readiness: coder: codex has not been told to trust /path/to/repo;
  it will stop at a trust prompt (blocked-trust)
```

`swarm bootstrap` records it, using Codex's own mechanism — the same
`[projects."<path>"] trust_level = "trusted"` entry in `~/.codex/config.toml`
that answering the prompt produces:

```
$ swarm bootstrap
✓ codex    workspace recorded as trusted
✓ coder    codex    ready (approval: autonomous)
READY
```

This is a separate, explicit command on purpose. Trusting a workspace is a
security decision, so `swarm start` will not make it quietly, and swarm never
simulates a keystroke to dismiss the prompt. Trust applies to the repository
root, which covers all four worktrees.

Interactive roles skip the check: a human is sitting there to answer.

## Backend readiness

`swarm doctor` and `swarm start` distinguish these:

| State | Meaning |
|---|---|
| `ready` | can start working |
| `blocked-trust` | will stop at the trust prompt — run `swarm bootstrap` |
| `blocked-approval` | the requested policy is not supported |
| `not-authenticated` | no stored credentials — run `codex login` |
| `missing` | `codex` is not on `PATH` |

"The process exists" is not readiness: an agent sitting at a prompt is running
and useless.

## Waking an agent

Codex reads its input asynchronously. Typing a line and pressing Enter in the
same instant leaves the line sitting unsent in its composer — the agent never
sees it. Swarm therefore types, waits for the composer to take the text, then
sends Enter (`tmux.SendPrompt`). This was a real bug: every delivery went
unnoticed while the daemon reported success.

## Known limitations

- **Liveness is a heuristic.** Swarm treats "the pane's foreground process is
  not a shell" as running, so any other program in a managed session reads as
  the agent.
- **Model latency is real.** A single role's turn can take minutes; scripts that
  wait on agent progress need generous timeouts.
- **Codex may still pause** for something swarm cannot anticipate. `swarm status`
  shows the notification state, and `swarm sessions capture <role>` shows the
  pane, which is the fastest way to see what an agent is waiting for.
- **No Claude backend yet.** The capability model exists so one can be added
  without touching the lifecycle.
