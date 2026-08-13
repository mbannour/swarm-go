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
go build ./...
```

Every example below uses `go run ./cmd/swarm …`, which works straight from the
source tree. If you would rather have a real command:

```bash
go build -o swarm ./cmd/swarm
./swarm version
```

Put `swarm` on your `PATH` and drop the `go run ./cmd/swarm` prefix everywhere.

## Quick start

Run these **from the root of the repository you want the agents to work on**.

```bash
# 0. one-time: the repo must have at least one commit
git add -A && git commit -m "initial commit"

# 1. four isolated worktrees + branches
go run ./cmd/swarm worktrees create

# 2. four tmux sessions, one per worktree
go run ./cmd/swarm sessions create

# 3. launch the agents with their role prompts
go run ./cmd/swarm agents start

# 4. see what is running
go run ./cmd/swarm agents list

# 5. in a second terminal: deliver messages between roles
go run ./cmd/swarm handoff daemon

# 6. sit down at the coder
go run ./cmd/swarm sessions attach coder
#    …work with it… then press Ctrl+b then d to detach
```

To shut down:

```bash
go run ./cmd/swarm agents stop        # stop agents, keep terminals
go run ./cmd/swarm sessions remove    # close the terminals
go run ./cmd/swarm worktrees remove   # remove the worktrees
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
        ├── specifier/
        │   ├── inbox/
        │   └── outbox/
        ├── coder/
        │   ├── inbox/
        │   └── outbox/
        ├── refactorer/…
        ├── architect/…
        ├── rejected/       # malformed messages + a .reason file each
        └── archive/        # acknowledged messages
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
- `prompts/roles/<role>.prompt` — that role's specific job.

When you run `agents start`, each agent gets:

```
# Swarm constitution
   …constitution.prompt…

# Role: coder
   …roles/coder.prompt…

# Runtime context
- role: coder
- repository: /home/you/your-project
- worktree: /home/you/your-project/.swarm/worktrees/wt-coder
- branch: swarm/coder
- receive mode: task
```

The assembled result is written to `.swarm/runtime/prompts/<role>.prompt`. Read
it to see exactly what an agent was told:

```bash
cat .swarm/runtime/prompts/coder.prompt
```

Edited a prompt? Just restart that agent — the file is regenerated on every
`agents start`:

```bash
go run ./cmd/swarm agents stop coder
go run ./cmd/swarm agents start coder
```

## Command reference

### Environment

```bash
swarm version     # print the version
swarm doctor      # check git, tmux and agent backends
swarm roles       # list the four-pack role names
swarm config      # parse and print swarm.conf
```

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
ROLE         BACKEND    SESSION              STATUS
specifier    codex      swarm-specifier      running
coder        codex      swarm-coder          running
refactorer   codex      swarm-refactorer     not-started
architect    codex      swarm-architect      session-missing
```

| Status | Meaning |
|---|---|
| `running` | something is in the foreground of the pane — assumed to be the agent |
| `not-started` | the session is up but sitting at a shell prompt |
| `session-missing` | no tmux session — run `swarm sessions create` |
| `backend-missing` | the configured backend isn't installed or isn't supported |

`agents stop` deliberately leaves the tmux sessions alive so you keep the
scrollback and can start again instantly.

### Handoffs

Roles talk to each other with **handoffs**: small text files that a background
daemon moves from a sender's outbox into the recipient's inbox.

```bash
swarm handoff send --from <role> --to <role> --type <type> --note "…"
swarm handoff outbox <role>       # queued, not yet delivered
swarm handoff daemon              # deliver continuously (Ctrl-C to stop)
swarm handoff inbox <role>        # delivered, waiting to be read
swarm handoff ack <role> <file>   # mark one as processed
```

Nothing is delivered until the daemon runs. Sending and reading never delete
anything by themselves — only `ack` moves a message out of an inbox.

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
and a commit:

```bash
swarm handoff send \
  --from coder --to refactorer \
  --type git_handoff \
  --task DEMO-1 \
  --commit abc123 \
  --priority 20 \
  --note "Ready for refactoring"
```

| Flag | Required | Meaning |
|---|---|---|
| `--from` | yes | sending role, must be in `swarm.conf` |
| `--to` | yes | destination role, must differ from `--from` |
| `--type` | yes | `note` or `git_handoff` (default `note`) |
| `--note` | yes | human-readable message; may be multi-line |
| `--priority` | no | `0`–`100`, higher is more urgent (default `10`) |
| `--task` | for `git_handoff` | task identifier, e.g. `AUTH-42` |
| `--commit` | for `git_handoff` | commit object name, 4–64 hex characters |

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
21:56:37  handoff daemon watching 4 roles every 250ms
21:56:37  delivered specifier -> coder (note, priority 10)
21:56:37  delivered coder -> refactorer (git_handoff, priority 20)
21:56:37  rejected coder/broken.handoff: destination role "foo" is not configured
```

One bad file never stops the daemon: it is moved to
`.swarm/handoffs/rejected/` next to a `.reason` file, and the pass continues.

#### Reading

```bash
swarm handoff inbox coder
```

```
INBOX: coder

PRIORITY  FROM        TYPE          TASK       FILE
20        specifier   git_handoff   AUTH-42    20260813T195637.053408567-specifier-to-coder.handoff
10        architect   note          -          20260813T195629.294523214-architect-to-coder.handoff
```

Highest priority first, then oldest first. `swarm handoff outbox coder` uses the
same format and is the place to look when a message hasn't arrived.

To read the message itself, open the file (it is plain text):

```bash
cat .swarm/handoffs/coder/inbox/*.handoff
```

```text
type: git_handoff
from: coder
to: refactorer
task: DEMO-1
commit: abc123
priority: 20
note: Ready for refactoring
```

Once you've dealt with it:

```bash
swarm handoff ack coder 20260813T195637.053408567-specifier-to-coder.handoff
```

which moves it to `.swarm/handoffs/archive/`.

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

# make sure the tools are there
go run ./cmd/swarm doctor

# stand everything up
go run ./cmd/swarm worktrees create
go run ./cmd/swarm sessions create
go run ./cmd/swarm agents start

# leave the daemon running in another terminal
go run ./cmd/swarm handoff daemon

# start with the specifier: describe the feature you want
go run ./cmd/swarm sessions attach specifier
#   → "Add rate limiting to the login endpoint…"
#   → it writes a spec and commits on branch swarm/specifier
#   → Ctrl+b d

# hand the spec to the coder
go run ./cmd/swarm handoff send \
  --from specifier --to coder \
  --type git_handoff --task RATE-1 \
  --commit "$(git -C .swarm/worktrees/wt-specifier rev-parse --short HEAD)" \
  --priority 20 --note "Specification ready; acceptance criteria in SPEC.md"
#   → the daemon delivers it and wakes the coder

go run ./cmd/swarm sessions attach coder
#   → the coder sees the wake-up, reads its inbox, merges swarm/specifier,
#     implements, tests, commits
#   → Ctrl+b d

# the coder hands the result on for cleanup
go run ./cmd/swarm handoff send \
  --from coder --to refactorer \
  --type git_handoff --task RATE-1 \
  --commit "$(git -C .swarm/worktrees/wt-coder rev-parse --short HEAD)" \
  --priority 20 --note "Implementation complete; tests pass"

# tidy up, then review
go run ./cmd/swarm sessions attach refactorer
go run ./cmd/swarm sessions attach architect
#   → the architect sends findings back with --type note

# take the finished work back to your branch
git merge swarm/coder
```

A handoff carries the *message*; the code still travels through Git. The
recipient merges the named branch or cherry-picks the named commit itself —
swarm does not merge anything for you. Automating that is a later milestone.

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

**`git_handoff requires a commit` / `commit "zzz" is not a hexadecimal object name`**
`--type git_handoff` needs `--task` and a `--commit` of 4–64 hex characters.
Use `--type note` for messages that carry no code.

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
internal/handoff  message model, parser, validation, store, delivery daemon
cmd/swarm         CLI wiring and output
```

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
  assumes it is the only daemon for a repository.
- **Handoffs carry messages, not code.** No automatic merge, cherry-pick, task
  state machine, or workflow advancement. `--commit` is checked for shape, not
  checked against the repository.
- **`ack` is manual**, and nothing re-notifies an agent that ignored a message.
- **New worktrees branch from current `HEAD`**, not from a fixed base branch.
- **POSIX shell assumed** in the tmux pane for the `"$(cat …)"` construct.
- **Not concurrency-safe** across simultaneous invocations in the same repo.
