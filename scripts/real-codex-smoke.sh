#!/usr/bin/env bash
#
# Real-agent smoke test: does an unattended swarm actually move with Codex?
#
#   task submit → specifier wakes → specifier hands off →
#   coder wakes → coder writes code and tests → tests pass →
#   coder commits → coder hands off → refactorer receives it
#
# It must reach that with ZERO manual input: no typing into tmux, no Enter, no
# approval prompts, no hand-written handoff files.
#
# This costs real model quota, so it is opt-in and strictly bounded:
#
#   RUN_REAL_CODEX_TESTS=1 ./scripts/real-codex-smoke.sh
#
# Never add it to normal CI.

set -uo pipefail

if [ "${RUN_REAL_CODEX_TESTS:-0}" != "1" ]; then
    cat <<'MSG'
This test calls a real AI backend and consumes quota, so it is opt-in:

  RUN_REAL_CODEX_TESTS=1 ./scripts/real-codex-smoke.sh

The deterministic suite (no model, no quota) is:

  ./scripts/parity.sh
MSG
    exit 0
fi

REPO_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_ID="${TASK_ID:-SMOKE-1}"

# Hard bounds. A stalled agent must fail fast, never idle burning quota.
BUDGET="${SMOKE_BUDGET:-240}"        # whole run, seconds
STEP_TIMEOUT="${STEP_TIMEOUT:-120}"  # any single wait, seconds
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
    for role in specifier coder; do
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

step "SPECIFIER"
wait_for "specifier accepts the task without manual input" \
    "[ -n \"\$(find '$REPO/.swarm/handoffs/specifier/current' -name '*.handoff' 2>/dev/null)\" ] || \
     [ -n \"\$(find '$REPO/.swarm/handoffs/specifier/completed' -name '*.handoff' 2>/dev/null)\" ]" || {
    diagnostics; exit 1; }

wait_for "specifier hands off to coder" \
    "[ -n \"\$(find '$REPO/.swarm/handoffs/coder/inbox' '$REPO/.swarm/handoffs/coder/current' -name '*.handoff' 2>/dev/null)\" ]" || {
    diagnostics; exit 1; }

step "CODER"
wait_for "coder accepts the handoff without manual input" \
    "[ -n \"\$(find '$REPO/.swarm/handoffs/coder/current' '$REPO/.swarm/handoffs/coder/completed' -name '*.handoff' 2>/dev/null)\" ]" || {
    diagnostics; exit 1; }

wait_for "coder commits real work" \
    "[ \"\$(git -C '$REPO/.swarm/worktrees/wt-coder' rev-list --count HEAD)\" -gt 1 ]" || {
    diagnostics; exit 1; }

if git -C "$REPO/.swarm/worktrees/wt-coder" show --name-only --format= HEAD | grep -q '\.go$'; then
    ok "the commit contains Go source"
else
    bad "the commit contains no Go source"
fi

if (cd "$REPO/.swarm/worktrees/wt-coder" && go test ./... >/dev/null 2>&1); then
    ok "the coder's tests pass"
else
    bad "the coder's tests do not pass"
fi

step "REFACTORER"
wait_for "refactorer receives the git handoff" \
    "grep -rlq '^type: git_handoff' '$REPO/.swarm/handoffs/refactorer/inbox' '$REPO/.swarm/handoffs/refactorer/current' 2>/dev/null" || {
    diagnostics; exit 1; }

step "NO MANUAL INTERVENTION"
# Nothing above typed into a pane or answered a prompt; if an agent were stuck
# on one, the waits would have timed out instead.
ok "the whole chain ran unattended"

# --------------------------------------------------------------------- verdict

step "TRACE"
"$SWARM" task trace "$TASK_ID" 2>/dev/null | head -30 | sed 's/^/  /'

echo
if [ "$FAIL" -eq 0 ]; then
    cat <<SUMMARY
REAL CODEX SMOKE PASSED

Task: $TASK_ID
Chain: task submit -> specifier -> coder -> refactorer
Manual input: none
Checks: $PASS passed, 0 failed
Elapsed: $(( $(date +%s) - STARTED_AT ))s
SUMMARY
    exit 0
fi

echo "REAL CODEX SMOKE FAILED ($FAIL failed, $PASS passed)"
diagnostics
exit 1
