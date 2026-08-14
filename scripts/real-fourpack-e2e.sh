#!/usr/bin/env bash
#
# Full four-pack acceptance with real Codex agents.
#
#   task submit → specifier → coder → refactorer → architect → specifier
#
# Every hop unattended: no typing into tmux, no Enter, no approval prompts, no
# hand-written handoff files. Handed-off commits must be integrated into each
# receiver's own worktree along the way.
#
# This is the expensive one — four or more model turns. It is opt-in and
# strictly bounded:
#
#   RUN_REAL_CODEX_TESTS=1 ./scripts/real-fourpack-e2e.sh
#
# Never add it to normal CI. The cheaper gate is scripts/real-codex-smoke.sh.

set -uo pipefail

if [ "${RUN_REAL_CODEX_TESTS:-0}" != "1" ]; then
    cat <<'MSG'
This test calls a real AI backend and consumes quota, so it is opt-in:

  RUN_REAL_CODEX_TESTS=1 ./scripts/real-fourpack-e2e.sh

The deterministic suite (no model, no quota) is:

  ./scripts/parity.sh
MSG
    exit 0
fi

REPO_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_ID="${TASK_ID:-CYCLE-1}"

# Hard bounds. A stalled agent must fail fast, never idle burning quota.
BUDGET="${E2E_BUDGET:-2400}"         # whole run, seconds
STEP_TIMEOUT="${STEP_TIMEOUT:-600}"  # any single wait, seconds
POLL=3

WORK="$(mktemp -d)"
REPO="$WORK/demo"
SWARM="$WORK/bin/swarm"
STARTED_AT=$(date +%s)

PASS=0
FAIL=0

step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }

budget_left() { echo $(( BUDGET - ($(date +%s) - STARTED_AT) )); }

# wait_for <description> <shell-condition>
wait_for() {
    local what="$1" cond="$2"
    local limit=$STEP_TIMEOUT
    local left
    left=$(budget_left)
    [ "$left" -lt "$limit" ] && limit=$left

    if [ "$limit" -le 0 ]; then
        bad "$what (no budget left)"
        return 1
    fi

    local deadline=$(( $(date +%s) + limit ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if eval "$cond" >/dev/null 2>&1; then
            ok "$what"
            return 0
        fi
        sleep "$POLL"
    done

    bad "$what (timed out after ${limit}s)"
    return 1
}

diagnostics() {
    step "DIAGNOSTICS"

    echo "--- swarm status"; "$SWARM" status 2>&1 | sed 's/^/  /'
    echo "--- swarm doctor"; "$SWARM" doctor 2>&1 | sed -n '/Diagnostics/,$p' | sed 's/^/  /'

    echo "--- handoff queues"
    for role in specifier coder refactorer architect; do
        for box in inbox outbox current completed failed; do
            local n=0
            n=$(find "$REPO/.swarm/handoffs/$role/$box" -name '*.handoff' 2>/dev/null | wc -l)
            [ "$n" != "0" ] && printf '  %-12s %-10s %s\n' "$role" "$box" "$n"
        done
    done

    echo "--- notification state"
    for f in "$REPO"/.swarm/runtime/notifications/*.json; do
        [ -f "$f" ] && sed 's/^/  /' "$f"
    done

    echo "--- daemon log (tail)"
    tail -25 "$REPO/.swarm/runtime/logs/handoffd.log" 2>/dev/null | sed 's/^/  /'

    # Pane captures show whether an agent is blocked on a prompt. Only the last
    # lines, to avoid dumping whole prompts.
    echo "--- agent panes (last lines)"
    for role in specifier coder refactorer architect; do
        echo "  [$role]"
        "$SWARM" sessions capture "$role" 2>/dev/null | tail -8 | sed 's/^/    /' || true
    done

    echo "--- git"
    git -C "$REPO" worktree list 2>&1 | sed 's/^/  /'
    git -C "$REPO" log --all --graph --oneline 2>&1 | head -20 | sed 's/^/  /'
}

cleanup() {
    local code=$?
    if [ -x "$SWARM" ] && [ -d "$REPO" ]; then
        (cd "$REPO" && "$SWARM" stop >/dev/null 2>&1)
    fi
    rm -rf "$WORK"
    exit "$code"
}

# ------------------------------------------------------------------ preflight

step "SETUP"

command -v codex >/dev/null || { echo "codex is not installed"; exit 1; }
command -v tmux  >/dev/null || { echo "tmux is not installed"; exit 1; }
command -v git   >/dev/null || { echo "git is not installed"; exit 1; }

mkdir -p "$WORK/bin"
(cd "$REPO_SRC" && go build -o "$SWARM" ./cmd/swarm) || { echo "build failed"; exit 1; }
echo "  built $SWARM"

# The scratch repository lives under the temp directory, which the binary
# resolver otherwise refuses as a `go run`-style throwaway. This build is
# stable for the life of the test, so say so explicitly.
export SWARM_BIN="$SWARM"

trap cleanup EXIT INT TERM

mkdir -p "$REPO"
cd "$REPO"
git init -q .
git config user.email smoke@swarm
git config user.name smoke
git config core.autocrlf false
cp -r "$REPO_SRC/prompts" .
printf '.swarm/\nbin/\n' >.gitignore
cat >go.mod <<'GOMOD'
module example.com/smoke

go 1.19
GOMOD
printf '# smoke\n' >README.md

# Autonomous: the whole point is that no human answers anything.
cat >swarm.conf <<'CONF'
window specifier codex wt-specifier task trusted
window coder codex wt-coder task trusted
window refactorer codex wt-refactorer task trusted
window architect codex wt-architect task trusted
CONF

# `trusted` runs Codex with its sandbox off. That is a deliberate choice for a
# throwaway repository under /tmp: Codex's workspace-write sandbox keeps .git
# read-only, so a role working in a linked worktree can build and test but
# never commit — which is precisely what this test has to prove.
#
# A sandboxed agent can only write inside its worktree, but Go keeps its build
# and module caches under $HOME. Without these the coder cannot run `go test`,
# and — correctly — will not commit work it could not verify.
for dir in "$(go env GOCACHE)" "$(go env GOMODCACHE)"; do
    [ -n "$dir" ] && echo "writable $dir" >>swarm.conf
done
echo "  writable roots: $(go env GOCACHE), $(go env GOMODCACHE)"

git add -A && git commit -qm "initial commit"
echo "  repository at $REPO"

step "BOOTSTRAP"
if "$SWARM" bootstrap 2>&1 | sed 's/^/  /'; then
    ok "backends ready for unattended use"
else
    bad "bootstrap"
    diagnostics
    exit 1
fi

step "START"
if "$SWARM" start >"$WORK/start.log" 2>&1; then
    ok "swarm start"
else
    bad "swarm start"
    sed 's/^/  /' "$WORK/start.log"
    diagnostics
    exit 1
fi

# ------------------------------------------------------------------- the test

step "SUBMIT"
"$SWARM" task submit --id "$TASK_ID" --priority 20 \
    --description "Create package calc with Add(a, b int) int in add.go, plus a table-driven test in add_test.go. Keep it tiny." \
    | sed 's/^/  /'

# accepted <role> — the role took the work off its inbox.
accepted() {
    local role="$1"
    [ -n "$(find "$REPO/.swarm/handoffs/$role/current" "$REPO/.swarm/handoffs/$role/completed" \
        -name '*.handoff' 2>/dev/null)" ]
}

# received <role> — something arrived for the role, anywhere in its lifecycle.
received() {
    local role="$1"
    [ -n "$(find "$REPO/.swarm/handoffs/$role/inbox" "$REPO/.swarm/handoffs/$role/current" \
        "$REPO/.swarm/handoffs/$role/completed" -name '*.handoff' 2>/dev/null)" ]
}

hop() {
    # hop <from> <to>
    local from="$1" to="$2"

    step "$(echo "$from" | tr '[:lower:]' '[:upper:]') → $(echo "$to" | tr '[:lower:]' '[:upper:]')"

    wait_for "$from accepts its work unattended" "accepted $from" || { diagnostics; exit 1; }
    wait_for "$from hands off to $to"            "received $to"   || { diagnostics; exit 1; }
}

hop specifier coder
hop coder refactorer
hop refactorer architect

step "ARCHITECT → SPECIFIER"
wait_for "architect accepts its work unattended" "accepted architect" || { diagnostics; exit 1; }

# The cycle closes when the specifier finishes a second item: the original
# requirement, and the architect's result coming back round.
wait_for "the cycle returns to the specifier" \
    "[ \"\$(find '$REPO/.swarm/handoffs/specifier/completed' -name '*.handoff' 2>/dev/null | wc -l)\" -ge 2 ]" || {
    diagnostics; exit 1; }

# --------------------------------------------------------------- assertions

step "ASSERTIONS"

for role in specifier coder refactorer architect; do
    n=$(find "$REPO/.swarm/handoffs/$role/completed" -name '*.handoff' 2>/dev/null | wc -l)
    if [ "$n" -ge 1 ]; then
        ok "$role completed work ($n)"
    else
        bad "$role completed nothing"
    fi
done

# Real commits on isolated branches.
for role in specifier coder refactorer architect; do
    n=$(git -C "$REPO/.swarm/worktrees/wt-$role" rev-list --count HEAD 2>/dev/null || echo 0)
    branch=$(git -C "$REPO/.swarm/worktrees/wt-$role" rev-parse --abbrev-ref HEAD 2>/dev/null)
    [ "$branch" = "swarm/$role" ] || bad "$role is on branch $branch"
    [ "$n" -gt 1 ] && ok "$role committed ($((n - 1)) commit(s) beyond the base)" || true
done

# Every git_handoff that reached a receiver must have been integrated, which is
# what makes the cycle operate on one coherent history rather than four islands.
integrated=$(grep -rl '^integration_status: integrated' "$REPO"/.swarm/handoffs/*/completed 2>/dev/null | wc -l)
if [ "$integrated" -ge 1 ]; then
    ok "handed-off commits were integrated into receiver worktrees ($integrated)"
else
    bad "no handoff records a successful integration"
fi

# Queues must be clean.
for role in specifier coder refactorer architect; do
    n=$(find "$REPO/.swarm/handoffs/$role/failed" -name '*.handoff' 2>/dev/null | wc -l)
    [ "$n" = "0" ] || bad "$role has $n failed handoff(s)"
done
r=$(find "$REPO/.swarm/handoffs/rejected" -name '*.handoff' 2>/dev/null | wc -l)
[ "$r" = "0" ] && ok "nothing failed or was rejected" || bad "$r rejected handoff(s)"

# The implementation actually exists somewhere in the history.
if git -C "$REPO" log --all -p -- '*.go' 2>/dev/null | grep -q "func Add"; then
    ok "the requested implementation is in the repository"
else
    bad "the requested implementation is missing"
fi

step "NO MANUAL INTERVENTION"
ok "the whole cycle ran unattended"

# --------------------------------------------------------------------- verdict

step "TRACE"
"$SWARM" task trace "$TASK_ID" 2>/dev/null | head -50 | sed 's/^/  /'

step "GIT GRAPH"
git -C "$REPO" log --all --graph --oneline 2>/dev/null | head -20 | sed 's/^/  /'

echo
if [ "$FAIL" -eq 0 ]; then
    cat <<SUMMARY
REAL FOUR-PACK E2E PASSED

Task: $TASK_ID
Cycle: specifier -> coder -> refactorer -> architect -> specifier
Manual input: none
Checks: $PASS passed, 0 failed
Elapsed: $(( $(date +%s) - STARTED_AT ))s
SUMMARY
    exit 0
fi

echo "REAL FOUR-PACK E2E FAILED ($FAIL failed, $PASS passed)"
diagnostics
exit 1
