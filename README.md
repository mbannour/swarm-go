# swarm-go

A small Go tool that runs a **four-pack** of AI coding agents on one repository
at the same time — each in its own Git worktree, on its own branch, inside its
own tmux session.

It is a Go reimplementation of SwarmForge's four-pack workflow. No Babashka, no
Clojure, no shell orchestration: one static binary and the standard library.

---

## Table of contents

- [What problem does this solve?](#what-problem-does-this-solve)
- [The four roles](#the-four-roles)
- [Requirements](#requirements)
- [Install](#install)
- [Quick start](#quick-start)
- [How it is laid out on disk](#how-it-is-laid-out-on-disk)
- [Configuration: `swarm.conf`](#configuration-swarmconf)
- [Prompts](#prompts)
- [Command reference](#command-reference)
  - [Handoffs](#handoffs)
  - [Routing work onward: `next`](#routing-work-onward-next)
  - [Send before done](#send-before-done)
- [tmux crash course](#tmux-crash-course)
- [A realistic session, end to end](#a-realistic-session-end-to-end)
- [Cleaning up](#cleaning-up)
- [Troubleshooting](#troubleshooting)
- [How it works internally](#how-it-works-internally)
- [Development](#development)
- [Current limitations](#current-limitations)

---

## What problem does this solve?

Running several AI agents on one checkout means they overwrite each other's
files and fight over the same branch.

`swarm-go` gives each agent **physical isolation**:

```
your repository
│
├── .swarm/worktrees/wt-specifier   ← branch swarm/specifier   ← tmux swarm-specifier
├── .swarm/worktrees/wt-coder       ← branch swarm/coder       ← tmux swarm-coder
├── .swarm/worktrees/wt-refactorer  ← branch swarm/refactorer  ← tmux swarm-refactorer
└── .swarm/worktrees/wt-architect   ← branch swarm/architect   ← tmux swarm-architect
```

Four real directories, four real branches, four long-lived terminals. They share
one Git history, so agents hand work to each other with ordinary commits.

## The four roles

| Role | Does | Does not |
|---|---|---|
| **specifier** | Turns requirements into explicit behavior, edge cases, acceptance criteria | Write production code |
| **coder** | Implements the spec, TDD where practical, runs tests, commits | Redesign the architecture |
| **refactorer** | Improves structure and names with behavior unchanged, tests still green | Change behavior |
| **architect** | Reviews boundaries, coupling, risks; produces actionable findings | Implement large changes |

Each role's instructions live in a plain text file you can edit — see
[Prompts](#prompts).

## Requirements

| Tool | Needed for | Check |
|---|---|---|
| Go 1.19+ | building the CLI | `go version` |
| Git 2.5+ | worktrees | `git --version` |
| tmux | sessions | `tmux -V` |
| An agent CLI | running agents | `codex --version` |

Only `codex` is wired up today. Git and tmux are required; the agent CLI is only
needed once you get to `agents start`.

Verify everything at once:

```bash
go run ./cmd/swarm doctor
```

```
Swarm environment check

✓ git        /usr/bin/git
✓ tmux       /usr/bin/tmux

Agent backends:
✓ codex      /home/you/.nvm/versions/node/v22.11.0/bin/codex
○ claude     not installed
```

`✓` present, `○` optional and absent, `✗` required and missing.

## Install

```bash
git clone <this-repo>
cd swarm-go
go build -o ./bin/swarm ./cmd/swarm
./bin/swarm version
```

⚠️ **Build a real binary before starting agents.** Agents are told the absolute
path of the swarm executable so they can drive their own lifecycle, and
`go run ./cmd/swarm` compiles to a throwaway binary under `/tmp` that
disappears the moment the command exits. `agents start` therefore refuses to
run under `go run`:

```
swarm is running from a temporary build (/tmp/go-build…/exe/swarm), which
agents cannot call after this process exits

build a stable binary first:
  go build -o ./bin/swarm ./cmd/swarm
  ./bin/swarm agents start
```

`go run ./cmd/swarm …` is fine for everything else, and the examples below use
`./bin/swarm`. Put it on your `PATH` if you prefer a bare `swarm`, or point
`SWARM_BIN` at any executable you want agents to use. `bin/` is gitignored.

## Quick start

Run these **from the root of the repository you want the agents to work on**.

```bash
# 0. one-time: the repo must have at least one commit, and swarm needs a
#    stable binary that agents can keep calling
git add -A && git commit -m "initial commit"
go build -o ./bin/swarm ./cmd/swarm

# 1. four isolated worktrees + branches
./bin/swarm worktrees create

# 2. four tmux sessions, one per worktree
./bin/swarm sessions create

# 3. launch the agents with their role prompts + the handoff protocol
./bin/swarm agents start

# 4. in a second terminal: deliver handoffs between roles, continuously
./bin/swarm handoff daemon

# 5. give the swarm something to do
./bin/swarm handoff send --from architect --to specifier \
  --type note --priority 20 --note "Add rate limiting to the login endpoint"

# 6. watch it flow
./bin/swarm status
```

From here the agents drive themselves: each one runs `handoff ready`, does its
role's job, sends the result to the next role with `handoff next`, and calls
`handoff done`. You can do the same by hand at any time — the commands are the
same ones the agents use.

To shut down:

```bash
./bin/swarm agents stop        # stop agents, keep terminals
./bin/swarm sessions remove    # close the terminals
./bin/swarm worktrees remove   # remove the worktrees
```

## How it is laid out on disk

```
your-project/
├── .git/
├── .gitignore              # ignores .swarm/
├── swarm.conf              # which roles exist  ← you edit this
├── prompts/                # what each role is told  ← you edit these
│   ├── constitution.prompt
│   └── roles/
│       ├── specifier.prompt
│       ├── coder.prompt
│       ├── refactorer.prompt
│       └── architect.prompt
└── .swarm/                 # generated, gitignored — never commit this
    ├── worktrees/
    │   ├── wt-specifier/
    │   ├── wt-coder/
    │   ├── wt-refactorer/
    │   └── wt-architect/
    ├── runtime/
    │   └── prompts/        # assembled prompts actually handed to agents
    │       ├── specifier.prompt
    │       └── …
    └── handoffs/           # durable messages between roles
        ├── coder/
        │   ├── outbox/     # queued, waiting for the daemon
        │   ├── sent/       # delivered to every destination
        │   ├── failed/     # valid, but could not be completed + .reason
        │   ├── inbox/      # delivered here, not yet accepted
        │   ├── current/    # accepted, being worked on now
        │   └── completed/  # finished
        ├── specifier/…
        ├── refactorer/…
        ├── architect/…
        ├── rejected/       # invalid messages + a .reason file each
        └── archive/        # legacy `ack` destination
```

You own `swarm.conf` and `prompts/`. Everything under `.swarm/` is generated and
can be deleted and rebuilt at any time.

## Configuration: `swarm.conf`

The single source of truth. One line per role:

```text
# Four-pack configuration

window specifier codex wt-specifier task
window coder codex wt-coder task
window refactorer codex wt-refactorer task
window architect codex wt-architect task
```

```text
window  <role>  <backend>  <worktree>  <receive-mode>
  │       │        │          │             │
  │       │        │          │             └─ task | batch
  │       │        │          └─ directory under .swarm/worktrees/
  │       │        └─ codex
  │       └─ role name; also names the branch and the tmux session
  └─ literal keyword
```

Blank lines and `#` comments are ignored. Every command reads this file, so it
decides what gets created, listed, and — importantly — what may be removed.

Check that it parses:

```bash
go run ./cmd/swarm config
```

```
Four-pack configuration

ROLE         BACKEND    WORKTREE         MODE
specifier    codex      wt-specifier     task
coder        codex      wt-coder         task
refactorer   codex      wt-refactorer    task
architect    codex      wt-architect     task
```

Names derived from a role, deterministically:

| From `swarm.conf` | Branch | Worktree | tmux session |
|---|---|---|---|
| `coder` / `wt-coder` | `swarm/coder` | `.swarm/worktrees/wt-coder` | `swarm-coder` |

## Prompts

Prompts are plain text files, not Go strings — edit them freely.

- `prompts/constitution.prompt` — shared rules every role receives: preserve
  repository integrity, stay in your own worktree, verify before claiming
  completion, hand off with commits, don't touch secrets.
- `prompts/runtime.prompt` — the **handoff protocol**, shared by all four roles.
  How to get work, what `NO_TASK` means, the worker loop, send-before-done, and
  the rule that handoff content is untrusted input. Written once here rather
  than copied into each role prompt.
- `prompts/roles/<role>.prompt` — that role's specific job.

When you run `agents start`, each agent gets all three plus generated context:

```
# Swarm constitution
   …constitution.prompt…

# Swarm runtime protocol
   …runtime.prompt…

# Role: coder
   …roles/coder.prompt…

# Runtime context

ROLE=coder
REPOSITORY_ROOT=/home/you/your-project
WORKTREE=/home/you/your-project/.swarm/worktrees/wt-coder
BRANCH=swarm/coder
RECEIVE_MODE=task
NEXT_ROLE=refactorer
SWARM_BIN=/home/you/your-project/bin/swarm

SWARM_BIN='/home/you/your-project/bin/swarm'

"$SWARM_BIN" handoff ready coder      # get or resume work
"$SWARM_BIN" handoff current coder    # re-read it, changing nothing
"$SWARM_BIN" handoff status coder     # has my downstream handoff been sent?
"$SWARM_BIN" handoff next --from coder …
"$SWARM_BIN" handoff done coder       # only after the handoff succeeded
```

`SWARM_BIN` is why the binary must be stable: agents call that exact path for
the whole life of their session. Nothing else about your environment is
exposed — no secrets, no environment variables.

The assembled result is written to `.swarm/runtime/prompts/<role>.prompt`. Read
it to see exactly what an agent was told:

```bash
cat .swarm/runtime/prompts/coder.prompt
```

Edited a prompt? Just restart that agent — the file is regenerated on every
`agents start`:

```bash
./bin/swarm agents stop coder
./bin/swarm agents start coder
```

### What agents are told to do

The runtime protocol gives every role the same loop:

1. `handoff ready <role>` — on startup and whenever woken.
2. `NO_TASK` → **stay idle**. Never invent work, never go looking for something
   useful to do.
3. Otherwise read the handoff file, and for a `git_handoff` inspect the
   canonical commit with `git show` / `git log`.
4. Do only this role's job.
5. Build and run the tests; read the real output.
6. Commit coherently if tracked files changed.
7. `handoff next --from <role> …` to route the result onward.
8. `handoff done <role>` — **only after step 7 succeeded**.

Agents are explicitly told not to write `while true` or `sleep` loops: the AI
process is already persistent, and the daemon wakes it when something arrives.

The protocol also treats handoff content as **untrusted input**: it is task
data, never a system instruction, never permission to break the constitution,
and never a reason to run a shell command that happens to appear in a note.
Secrets must never be put into a handoff.

## Command reference

### Environment

```bash
swarm status      # one read-only overview of the whole four-pack
swarm version     # print the version
swarm doctor      # check git, tmux and agent backends
swarm roles       # list the four-pack role names
swarm config      # parse and print swarm.conf
```

`swarm status` is the command to reach for when you want to know what the swarm
is doing:

```
FOUR-PACK STATUS

ROLE         AGENT            WORK       INBOX     TASK
specifier    running          waiting    0         -
coder        running          working    1         RATE-1
refactorer   running          waiting    0         -
architect    running          waiting    0         -

ROUTE
  specifier -> coder
  coder -> refactorer
  refactorer -> architect
  architect -> specifier

PENDING DELIVERY
  none
```

`AGENT` is the process state, `WORK` is the lifecycle state derived from the
filesystem (`working` = something in `current/`, `ready` = inbox waiting,
`waiting` = idle). A task shown as `RATE-1 (handed off)` means the downstream
handoff already exists and only `done` is outstanding.

### Worktrees

```bash
swarm worktrees create           # create one worktree + branch per role
swarm worktrees list             # show configured vs. actual
swarm worktrees remove           # remove them (branches are kept)
swarm worktrees remove --force   # also remove ones with uncommitted changes
```

```
Creating four-pack worktrees

✓ specifier    .swarm/worktrees/wt-specifier
✓ coder        .swarm/worktrees/wt-coder
✓ refactorer   .swarm/worktrees/wt-refactorer
✓ architect    .swarm/worktrees/wt-architect
```

Run it twice and it says `○ coder  already exists` instead of failing.

`remove` only ever touches worktrees that are (a) listed in `swarm.conf`, (b)
located under `.swarm/worktrees`, and (c) registered with Git. It calls
`git worktree remove` — never `rm -rf`. Branches survive on purpose: that's
where the agents' work is. Delete one yourself with `git branch -D swarm/coder`.

### Sessions

```bash
swarm sessions create        # one detached tmux session per role
swarm sessions list          # running / missing
swarm sessions attach coder  # attach your terminal to one role
swarm sessions remove        # kill this project's sessions only
```

```
ROLE         SESSION              STATUS
specifier    swarm-specifier      running
coder        swarm-coder          running
refactorer   swarm-refactorer     running
architect    swarm-architect      missing
```

Each session starts inside its role's worktree, so `pwd` inside
`swarm-coder` prints `…/.swarm/worktrees/wt-coder`.

### Agents

```bash
swarm agents start         # launch the backend in every session
swarm agents start coder   # …or just one role
swarm agents list          # status per role
swarm agents stop          # Ctrl-C the agents, keep the sessions
swarm agents stop coder
```

```
ROLE         BACKEND    SESSION              AGENT            WORK
specifier    codex      swarm-specifier      running          waiting
coder        codex      swarm-coder          running          working
refactorer   codex      swarm-refactorer     not-started      ready
architect    codex      swarm-architect      session-missing  waiting
```

`AGENT` is the process; `WORK` is what the handoff lifecycle says the role is
doing. They are independent: an agent can be `running` with nothing to do, or
have work waiting while its session is down.

| Status | Meaning |
|---|---|
| `running` | something is in the foreground of the pane — assumed to be the agent |
| `not-started` | the session is up but sitting at a shell prompt |
| `session-missing` | no tmux session — run `swarm sessions create` |
| `backend-missing` | the configured backend isn't installed or isn't supported |

`agents stop` deliberately leaves the tmux sessions alive so you keep the
scrollback and can start again instantly.

### Handoffs

Roles pass work to each other with **handoffs**: small text files that a
background daemon validates and moves from a sender's outbox into the
recipient's inbox, where the recipient accepts them one task (or one batch) at
a time.

```bash
swarm handoff daemon          # validate + deliver continuously (Ctrl-C to stop)

# receiving — what an agent runs
swarm handoff ready <role>    # accept work, or resume what you were doing
swarm handoff current <role>  # re-read active work, changing nothing
swarm handoff status <role>   # work state + has the downstream handoff been sent?
swarm handoff next --from <role> …   # route the result to the next role
swarm handoff done <role>     # finish current work, pick up the next

# sending and browsing — mostly for humans
swarm handoff send --from <role> --to <role> --type <type> --note "…"
swarm handoff inbox <role>    # delivered, not yet accepted
swarm handoff outbox <role>   # queued, not yet delivered
```

Nothing is delivered until the daemon runs, and nothing is accepted until
someone calls `ready`.

#### The lifecycle

Every message moves through directories, so the state of the world is always
visible with `ls` and always survives a crash or restart.

```
                    ┌── message is invalid ─────→ rejected/       + .reason
outbox ─── daemon ──┤
   │                └── valid, unfulfillable ───→ <sender>/failed/ + .reason
   │                    (commit won't resolve, inbox unwritable)
   └── delivered to every destination ─────────→ <sender>/sent/

<recipient>/inbox ──ready──→ current ──done──→ completed
                                ↑                  │
                                └── done picks up the next work
```

Two things worth internalising:

- **Outbound messages are never deleted.** They end up in `sent/` or `failed/`,
  so you can always audit what a role sent and what became of it.
- **`rejected` ≠ `failed`.** *Rejected* means the message itself is at fault —
  unknown role, bad type, missing field, wrong outbox, unparsable file. That is
  permanent and never retried. *Failed* means a perfectly well-formed request
  that could not be completed — most often a commit that does not resolve — and
  it lands in the sender's own `failed/` box to be fixed and re-sent.

#### Sending

A **note** is lightweight coordination:

```bash
swarm handoff send \
  --from specifier --to coder \
  --type note \
  --priority 10 \
  --note "Implement the approved specification"
```

A **git_handoff** transfers implementation work and additionally requires a task
and a **real commit, abbreviated to exactly 10 characters**:

```bash
swarm handoff send \
  --from coder --to refactorer \
  --type git_handoff \
  --task DEMO-1 \
  --commit "$(git rev-parse --short=10 HEAD)" \
  --priority 20 \
  --note "Ready for refactoring"
```

One message can go to several roles at once — each gets its own independent
copy and its own lifecycle:

```bash
swarm handoff send --from coder --to refactorer,architect \
  --type note --priority 15 --note "Please review the new cache layer"
```

| Flag | Required | Meaning |
|---|---|---|
| `--from` | yes | sending role, must be in `swarm.conf` |
| `--to` | yes | one role, or several separated by commas; never `--from` itself |
| `--type` | yes | `note` or `git_handoff` (default `note`) |
| `--note` | yes | human-readable message; may be multi-line |
| `--priority` | no | `0`–`100`, higher is more urgent (default `10`) |
| `--task` | for `git_handoff` | task identifier, e.g. `AUTH-42` |
| `--commit` | for `git_handoff` | exactly 10 hex characters, and it must exist |

Notes carry no Git identity: passing `--task` or `--commit` with `--type note`
is an error rather than being silently ignored.

`id`, `created_at`, `delivered_at` and `canonical_commit` are generated by
swarm, never accepted from the sender.

#### How commits are verified

A `git_handoff` names work that actually exists. The daemon checks it in two
steps:

1. **Shape** — exactly 10 hexadecimal characters (either case). `abc123` and
   `71ae82cc13ZZ` are rejected without Git being consulted.
2. **Existence** — the daemon runs, against *this project's* repository:

   ```bash
   git rev-parse --verify --end-of-options <commit>^{commit}
   ```

   The `^{commit}` peel means a real object that is not a commit (a blob, a
   tree) is refused too. The full canonical SHA comes back and is stamped into
   every delivered copy as `canonical_commit`.

A handoff can never choose which repository it is checked against — that comes
from the project, not the message. A well-formed commit that does not resolve
is a *failed* delivery, not a rejected message:

```
22:21:15  failed refactorer/…-refactorer.handoff: commit ffffffffff does not
          resolve to a commit in this repository
```

Swarm verifies and communicates Git identity only. It never cherry-picks or
merges for you — the recipient inspects the canonical commit and decides.

#### The daemon

```bash
swarm handoff daemon                    # scan every 250ms
swarm handoff daemon --interval 1s      # slower
swarm handoff daemon --quiet            # deliver, but don't wake agents
```

Leave it running in its own terminal while you work. Every pass it scans each
role's outbox, validates what it finds, delivers valid messages to the
destination inbox, quarantines invalid ones, and wakes the recipient's agent.
It stops cleanly on <kbd>Ctrl</kbd>+<kbd>C</kbd>.

```
22:21:15  handoff daemon watching 4 roles every 250ms
22:21:15  delivered specifier -> coder (git_handoff, priority 20, id 33a9cc22)
22:21:15  failed refactorer/…-refactorer.handoff: commit ffffffffff does not
          resolve to a commit in this repository
22:21:15  delivered architect -> coder (note, priority 10, id 49de9d62)
```

One bad message never stops the daemon: it is moved aside with a `.reason`
file and the pass continues, as above where two valid messages were delivered
in the same scan.

Delivery is idempotent. Every handoff carries a random 128-bit `id`, the
destination filename derives from it, and the daemon checks the recipient's
`inbox/`, `current/` **and** `completed/` before writing. A retry after a crash
finds the earlier copy and reports it as already delivered instead of
duplicating it. Transient filesystem errors are retried a few times; a failed
tmux wake-up is only logged, never rolled back, because the message is already
durable — the recipient still finds it with `handoff ready`.

#### Browsing

```bash
swarm handoff inbox coder    # delivered, not yet accepted
swarm handoff outbox coder   # queued, not yet delivered
```

```
INBOX: coder

PRIORITY  FROM        TYPE          TASK       FILE
20        specifier   git_handoff   AUTH-42    20260813T202051…-specifier-to-coder.handoff
10        architect   note          -          20260813T202052…-architect-to-coder.handoff
```

Highest priority first, then oldest first. Browsing never changes anything — it
is for humans debugging the queue. `outbox` is where to look when a message
hasn't arrived.

#### Receiving work: `ready` and `done`

`ready` is how a role picks up work. It moves the selected message(s) from
`inbox/` into `current/` and prints them as stable `KEY: value` lines that both
humans and agents can read:

```bash
swarm handoff ready coder
```

```
TASK: /…/.swarm/handoffs/coder/current/20260813T202051…-specifier-to-coder.handoff
ID: 33a9cc22bb28056ad1ffc1a792d24f0e
TYPE: git_handoff
FROM: specifier
PRIORITY: 20
TASK_NAME: AUTH-42
COMMIT: 81fd839ede
CANONICAL_COMMIT: 81fd839edebf9dc25fe6999c7e961b3a14eeb497
CREATED_AT: 2026-08-13T20:20:51Z
DELIVERED_AT: 2026-08-13T20:21:15Z
MESSAGE: Specification ready
```

A note prints the same shape without the Git lines. With nothing to do, the
entire output is:

```
NO_TASK
```

Calling `ready` again returns **the same work**, and selects nothing new, until
you finish it. That is what stops a task being picked up twice, and it is also
how a crash is recovered: state lives in `current/` on disk, so a fresh process
sees exactly what the old one was doing.

When the work is finished:

```bash
swarm handoff done coder
```

```
DONE: AUTH-42
TASK: /…/.swarm/handoffs/coder/current/20260813T202052…-architect-to-coder.handoff
TYPE: note
FROM: architect
PRIORITY: 10
MESSAGE: Please clarify retry semantics
```

`done` moves everything in `current/` to `completed/` and immediately selects
the next available work, so an agent can loop on `done` alone. With nothing
left it prints `DONE: …` followed by `NO_TASK`; with nothing in progress it
prints `NO_CURRENT_WORK`.

#### Routing work onward: `next`

`handoff next` is how a role passes its finished work to the next role. It
knows the four-pack route, so no `--to` is needed:

```
specifier → coder → refactorer → architect → specifier
```

```bash
swarm handoff next --from coder \
  --type git_handoff \
  --task RATE-1 \
  --commit "$(git rev-parse --short=10 HEAD)" \
  --priority 20 \
  --note "Implementation complete; tests pass"
```

```
SENT
ID: 4b03562ec6dd2e14c33e2188de3fc921
SOURCE_ID: 8d21f539860a2a7d3816ba25d481ee71
TO: refactorer
TYPE: git_handoff
FILE: /…/.swarm/handoffs/coder/outbox/…-coder.handoff
```

`SOURCE_ID` links the new message back to the work that produced it, and that
link is what makes **`next` safe to re-run**. Ask twice for the same current
work and you get the original message back, not a second one:

```
ALREADY_SENT
ID: 4b03562ec6dd2e14c33e2188de3fc921
```

`--to` overrides the route if you need to send somewhere else; `handoff send`
remains available for messages unrelated to your current work.

#### Send before done

The order matters:

```
work → verify → commit → handoff next → handoff done
```

`done` is the **last** step. If `next` fails, the work stays in `current/` so
you can fix the problem and retry. Running `done` on a failed send would drop
the work with nothing downstream to continue it.

This also makes crashes recoverable. If the process dies between `next` and
`done`:

```bash
swarm handoff ready coder     # returns the same task — you never lost it
swarm handoff status coder
```

```
STATE: working
CURRENT_ID: 8d21f539860a2a7d3816ba25d481ee71
DOWNSTREAM_SENT: yes
DOWNSTREAM_ID: 4b03562ec6dd2e14c33e2188de3fc921
DOWNSTREAM_TO: refactorer
```

`DOWNSTREAM_SENT: yes` means go straight to `done`. And if you re-run `next`
anyway, you get `ALREADY_SENT` — the protection is in the orchestrator, not in
an agent remembering what it did.

#### Inspecting without changing anything

```bash
swarm handoff current coder   # the active work
swarm handoff status coder    # state summary
```

```
CURRENT: /…/.swarm/handoffs/coder/current/…-specifier-to-coder.handoff
ID: 8d21f539860a2a7d3816ba25d481ee71
TYPE: git_handoff
FROM: specifier
PRIORITY: 20
TASK_NAME: RATE-1
COMMIT: 16c604b31a
CANONICAL_COMMIT: 16c604b31a3da75b0eb6c6059cfacce908984ccb
MESSAGE: Implementation complete; tests pass
```

Neither moves anything. `current` prints `NO_CURRENT_WORK` when idle. Both are
the commands to run after a restart when you are not sure where you left off.

#### Receive modes: task and batch

The last column of `swarm.conf` decides how a role takes work.

```text
window coder codex wt-coder task          ← one message at a time
window refactorer codex wt-refactorer batch  ← everything at the top priority
```

**`task`** selects exactly one message: highest priority, then oldest, with a
deterministic tie-breaker. Everything else waits in the inbox.

**`batch`** selects every message sharing the *highest available* priority.
Given an inbox of 20, 20, 10:

```
BATCH: 4ad8f0814b842f001b15de7518414171
PRIORITY: 20
BATCH_ITEM: /…/current/20260813T202207…-coder-to-refactorer.handoff
BATCH_ITEM: /…/current/20260813T202207…-coder-to-refactorer.handoff

TASK: …            ← then each item in full, one block per item
```

The priority-10 message stays put, and a higher-priority message arriving
mid-batch does **not** join or displace the active batch — it waits for the
next one. `done` completes the whole batch at once.

#### `ack` (deprecated)

```bash
swarm handoff ack coder <filename>   # moves inbox → archive/
```

The Step 6 command, kept so old scripts keep working. It prints a deprecation
warning. Use `ready`/`done` instead — they are the real lifecycle.

#### What agents see

After a delivery, the daemon types one fixed sentence into the recipient's tmux
session:

> A new handoff is available in your inbox. Inspect it before continuing.

That is deliberately all. The message content lives in the file, where it
survives a crash, a detach, or a restarted agent — tmux is only used to ring the
bell. No task, commit, or note text is ever typed into a terminal.

## tmux crash course

If you have never used tmux, this is all you need.

tmux keeps terminals running after you walk away. You *attach* to look at one and
*detach* to leave it running.

| Keys | Does |
|---|---|
| <kbd>Ctrl</kbd>+<kbd>b</kbd> then <kbd>d</kbd> | **detach** — leaves the agent running |
| <kbd>Ctrl</kbd>+<kbd>b</kbd> then <kbd>[</kbd> | scroll back (<kbd>q</kbd> to exit scroll mode) |
| <kbd>Ctrl</kbd>+<kbd>b</kbd> then <kbd>?</kbd> | tmux's own help |

⚠️ Detach with <kbd>Ctrl</kbd>+<kbd>b</kbd> <kbd>d</kbd>. Typing `exit` closes the
session, and you'd have to recreate it.

`swarm-go` uses its **own private tmux server**, not your normal one, so nothing
here can disturb tmux sessions you already had open. The socket is derived from
the repository path:

```
/tmp/swarm-go-<your-username>/<12-hex-project-id>.sock
```

Same repository → same socket, every time. Different repository → different
socket, so two projects never collide. Because it is a separate server, plain
`tmux ls` will **not** show swarm sessions — use `swarm sessions list`.

## A realistic session, end to end

```bash
cd ~/projects/my-app

# make sure the tools are there, and build the binary agents will call
go run ./cmd/swarm doctor
go build -o ./bin/swarm ./cmd/swarm

# stand everything up
./bin/swarm worktrees create
./bin/swarm sessions create
./bin/swarm agents start

# leave the daemon running in another terminal
./bin/swarm handoff daemon

# hand the requirement to the specifier and let the swarm run
./bin/swarm handoff send --from architect --to specifier \
  --type note --priority 20 \
  --note "Add rate limiting to the login endpoint: 5 req/min per account"

./bin/swarm status
```

From here each agent does this for itself, because the runtime protocol told it
to:

```bash
# inside the specifier's session
"$SWARM_BIN" handoff ready specifier      # picks up the requirement
#   …writes the spec, commits on swarm/specifier…
"$SWARM_BIN" handoff next --from specifier \
  --type git_handoff --task RATE-1 \
  --commit "$(git rev-parse --short=10 HEAD)" \
  --priority 20 --note "Specification ready; acceptance criteria in SPEC.md"
"$SWARM_BIN" handoff done specifier

# the daemon delivers, the coder is woken, and it runs the same three commands
# — ready, next (routed to refactorer), done — then the refactorer, then the
# architect, whose note routes back to the specifier.
```

You can run any of those commands yourself to watch, nudge, or take over:

```bash
./bin/swarm handoff current coder     # what is the coder working on?
./bin/swarm handoff status coder      # has it handed off yet?
./bin/swarm status                    # the whole picture

# take the finished work back to your branch
git merge swarm/coder
```

A handoff carries the *message* and the commit's identity; the code still
travels through Git. The recipient merges the named branch or cherry-picks the
named commit itself — swarm does not merge anything for you. Automating that is
a later milestone.

## Cleaning up

```bash
# Ctrl-C the handoff daemon first
go run ./cmd/swarm agents stop        # 1. stop the agents
go run ./cmd/swarm sessions remove    # 2. close the tmux sessions
go run ./cmd/swarm worktrees remove   # 3. remove the worktrees
```

Undelivered and archived handoffs survive under `.swarm/handoffs/`; delete that
directory if you want a clean slate.

Branches remain after step 3 — that is where the work is. Once you've merged or
abandoned it:

```bash
git branch -D swarm/specifier swarm/coder swarm/refactorer swarm/architect
```

## Troubleshooting

**`cannot create worktrees: repository has no commits yet`**
A worktree needs a commit to branch from. `git add -A && git commit -m "initial commit"`.

**`not inside a git repository`**
Run the command from inside the repository you want to work on.

**`worktree …/wt-coder does not exist`**
Run `swarm worktrees create` before `sessions create`.

**`tmux session swarm-coder is not running`**
Run `swarm sessions create` before `agents start`.

**`tmux is not installed or not available in PATH`**
Install tmux (`apt install tmux`, `brew install tmux`).

**`codex backend is configured for role "coder" but executable was not found in PATH`**
Install the Codex CLI, or point `swarm.conf` at a backend you have.

**`unsupported backend "foo" for role "coder"`**
Only `codex` is supported right now. Fix the third column of `swarm.conf`.

**`swarm sessions list` says running, but `tmux ls` shows nothing**
Expected — swarm uses its own tmux socket. See [tmux crash course](#tmux-crash-course).

**`.swarm/worktrees/wt-coder already exists but is not a registered worktree`**
A leftover directory Git doesn't know about. Move it aside and retry; swarm
refuses to delete directories it didn't create.

**`git worktree remove` refuses**
The worktree has uncommitted changes. Commit them, or
`swarm worktrees remove --force` to discard.

**A handoff never arrives**
The daemon has to be running. Check `swarm handoff outbox <sender>`: if the
message is still there, start `swarm handoff daemon`. If it is in neither box,
look in `.swarm/handoffs/rejected/` for a `.reason` file.

**`destination role "foo" is not configured`**
Both `--from` and `--to` must name a role in `swarm.conf`, and they must differ.

**`commit "abc123" must be exactly 10 hexadecimal characters`**
Use `git rev-parse --short=10 <ref>`. Full SHAs and 7-character abbreviations
are both refused.

**`commit ffffffffff does not resolve to a commit in this repository`**
The abbreviation is well formed but names nothing here — usually a typo, or a
commit that only exists in a worktree branch you have not fetched. The message
is waiting in the sender's `failed/` box with a `.reason`; fix it and re-send.

**`note must not carry task or commit fields`**
Use `--type git_handoff` when you want to reference a commit.

**`ready` keeps returning the same task**
That is deliberate — work in `current/` is yours until `swarm handoff done
<role>`. It is also how a crash recovers.

**`done` prints `NO_CURRENT_WORK`**
Nothing was accepted. Run `swarm handoff ready <role>` first.

**`swarm is running from a temporary build …`**
`agents start` was run under `go run`, whose binary disappears when the command
exits. Build one agents can keep calling: `go build -o ./bin/swarm ./cmd/swarm`,
then `./bin/swarm agents start`. Or set `SWARM_BIN` to an existing executable.

**`handoff next` prints `ALREADY_SENT`**
Working as intended: this current work already produced a downstream handoff,
usually because you were interrupted after sending. Run `handoff done <role>`.

**`role "coder" has no current work to hand off`**
`handoff next` sends *your current work* onward, so accept something with
`handoff ready` first. For an unrelated message, use `handoff send`.

**An agent sits idle with work in its inbox**
Its wake-up may have been missed (a notification is best-effort). `swarm status`
will show `ready`; attach to the session and let the agent run
`handoff ready <role>`.

**An agent invents work, or ignores the lifecycle**
Check what it was actually told: `cat .swarm/runtime/prompts/<role>.prompt`.
Prompts are regenerated on every `agents start`, so edit
`prompts/runtime.prompt` and restart that agent.

**`rejected …: file is not a valid handoff`**
Something wrote a malformed file into an outbox. Prefer `swarm handoff send`,
which writes atomically; a hand-written file caught mid-write reads as garbage.

## How it works internally

One package per responsibility, with deliberately narrow seams:

```
internal/config   parses swarm.conf — decides what should exist
internal/git      worktrees and branches (via os/exec git)
internal/tmux     sessions on a private socket (via os/exec tmux)
internal/prompt   loads and assembles instructions
internal/agent    launches/stops agents; backend adapters
internal/handoff  message model, parser, validation, durable store,
                  priority selector, ready/done lifecycle, delivery daemon
cmd/swarm         CLI wiring and output
```

Inside `internal/handoff` the split is deliberate: `store.go` owns the
filesystem, `selector.go` is pure priority logic with no I/O, `lifecycle.go`
implements ready/done/advance, `route.go` is the *only* place the four-pack
route is written down, and `daemon.go` only does outbound delivery. Commit
resolution lives in `internal/git`, behind a `CommitResolver` interface the
daemon depends on — the handoff packages never shell out to Git themselves.

Rules that keep it honest: the agent layer never creates worktrees, the prompt
layer knows nothing about tmux, the tmux layer knows nothing about Codex, and
the handoff layer knows nothing about either — it wakes agents through a
`Notifier` interface the CLI supplies.

Everything derived from a role name is deterministic and unit-tested — branch
`swarm/<role>`, path `.swarm/worktrees/<worktree>`, session `swarm-<role>`,
socket id `sha256(repo-root)[:12]`. Nothing random, so repeated runs converge
instead of piling up.

Adding a backend means implementing three methods:

```go
type Backend interface {
    Name() string                               // identifier in swarm.conf
    Executable() string                         // what must be in PATH
    Command(promptPath, workdir string) string  // shell line for the pane
}
```

Codex is launched as:

```sh
codex --cd '<worktree>' "$(cat '<runtime prompt path>')"
```

The prompt *text* never travels through `tmux send-keys` — only two fixed,
single-quoted paths do, and the pane's shell reads the file locally.

Handoff delivery is atomic. Sending writes a temp file in the target directory,
syncs it, and renames it into place; delivery is a single `os.Rename` from
outbox to inbox. A message is therefore always in exactly one box and never
half-written. Role names read from a handoff file are only ever *looked up* in
the configured set — they never become path components — so no message can name
a directory you did not configure, and nothing in a handoff is ever executed or
interpolated into a shell command.

Safety properties worth knowing: no `rm -rf` anywhere; removal is restricted to
paths under `.swarm/worktrees` that Git already tracks and `swarm.conf` names;
`tmux kill-session` only targets this project's `swarm-*` sessions on this
project's socket; all interpolated paths are shell-quoted.

## Development

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l ./cmd ./internal
```

Tests never touch your real repository or your default tmux server: the Git
tests build a throwaway repo in `t.TempDir()`, and the tmux tests use their own
socket and kill it afterwards. Both skip cleanly when the tool is missing, and
no test launches a real agent.

## Current limitations

- **Only Codex.** `claude` and others are not wired up yet.
- **Agent status is a heuristic.** Codex runs as a Node process, so swarm reports
  `running` when the pane's foreground process is *not* a shell. Any other
  program you leave running in a managed session also reads as `running`.
- **`agents stop` sends a single Ctrl-C.** If the agent ignores it or asks for
  confirmation, it may survive; check with `agents list`.
- **Handoff delivery needs the daemon running.** Nothing moves while it is
  stopped; messages simply wait in the outbox.
- **The daemon polls every 250 ms** rather than watching the filesystem, and
  assumes it is the only daemon for a repository. Delivery is idempotent, but
  there is no lockfile, so two daemons would race on the source file.
- **Handoffs carry Git identity, not code.** The commit is verified and passed
  on; no automatic merge, cherry-pick, task state machine, or workflow
  advancement.
- **A multi-destination message has one shared fate for its source file** —
  all-delivered goes to `sent/`, anything else to `failed/` with the partial
  outcome recorded. Successful copies are never rolled back, but there is no
  retry of just the failed leg; you re-send.
- **`done` completes whatever is in `current/`** without checking that the work
  was actually done, and does not refuse when `DOWNSTREAM_SENT: no`. The
  send-before-done order is enforced by the prompt and made visible by
  `handoff status`, not by the orchestrator.
- **The route is fixed in code**, not per-project configuration — one place
  (`internal/handoff/route.go`), but not data-driven.
- **Duplicate protection is per piece of current work.** A role that genuinely
  wants to send two different downstream messages from one task gets the first
  one back from `handoff next`; use `handoff send` for that.
- **A batch produces one downstream handoff**, not one per item: the batch's
  `source_handoff_id` is its first item's id.
- **`WORK` state comes from the filesystem**, so a wedged agent still shows
  `working`.
- **Whether Codex reliably follows the runtime protocol is empirical.** The
  orchestrator makes the lifecycle safe to retry and impossible to duplicate,
  but it cannot force an agent to run the commands.
- **Nothing re-notifies** an agent that ignored a wake-up; `ready` is the
  recovery path.
- **`ack` is deprecated** but still present, so two lifecycles technically
  coexist until it is removed.
- **New worktrees branch from current `HEAD`**, not from a fixed base branch.
- **POSIX shell assumed** in the tmux pane for the `"$(cat …)"` construct.
- **Not concurrency-safe** across simultaneous invocations in the same repo.
