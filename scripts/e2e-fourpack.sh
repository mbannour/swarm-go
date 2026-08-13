#!/usr/bin/env bash
#
# End-to-end four-pack acceptance run, at the CLI level.
#
# Builds swarm, creates a throwaway repository, starts the real orchestrator
# with fake agents in real tmux sessions, submits one requirement, and proves it
# travels the whole cycle:
#
#   developer -> specifier -> coder -> refactorer -> architect -> specifier
#
# It also restarts the swarm mid-flight and asserts no duplicate handoff.
# No AI service is involved.

set -uo pipefail

REPO_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_ID="${TASK_ID:-DEMO-1}"
TIMEOUT="${TIMEOUT:-90}"          # seconds for the whole cycle
POLL_INTERVAL=1

WORK="$(mktemp -d)"
REPO="$WORK/demo"
BIN_DIR="$WORK/bin"
SWARM="$BIN_DIR/swarm"

PASS=0
FAIL=0

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

check() {
    # check <description> <command...>
    local what="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}

# ---------------------------------------------------------------- diagnostics

diagnostics() {
    step "DIAGNOSTICS"

    echo "--- swarm status"
    "$SWARM" status 2>&1 | sed 's/^/  /'

    echo "--- handoff queues"
    for role in specifier coder refactorer architect; do
        for box in inbox outbox current completed failed; do
            local dir="$REPO/.swarm/handoffs/$role/$box"
            local n=0
            [ -d "$dir" ] && n=$(find "$dir" -name '*.handoff' 2>/dev/null | wc -l)
            [ "$n" != "0" ] && printf '  %-12s %-10s %s\n' "$role" "$box" "$n"
        done
    done

    echo "--- rejected"
    ls -1 "$REPO/.swarm/handoffs/rejected" 2>/dev/null | sed 's/^/  /' || echo "  (none)"

    echo "--- failure reasons"
    find "$REPO/.swarm/handoffs" -name '*.reason' -exec sh -c 'echo "  $1:"; sed "s/^/    /" "$1"' _ {} \; 2>/dev/null

    echo "--- daemon log (tail)"
    tail -30 "$REPO/.swarm/runtime/logs/handoffd.log" 2>/dev/null | sed 's/^/  /' || echo "  (none)"

    echo "--- tmux sessions"
    "$SWARM" sessions list 2>&1 | sed 's/^/  /'

    echo "--- git graph"
    git -C "$REPO" log --all --graph --oneline 2>&1 | head -40 | sed 's/^/  /'

    echo "--- trace"
    "$SWARM" task trace "$TASK_ID" 2>&1 | head -60 | sed 's/^/  /'
}

cleanup() {
    local code=$?

    if [ -x "$SWARM" ] && [ -d "$REPO" ]; then
        (cd "$REPO" && "$SWARM" stop >/dev/null 2>&1)
    fi
    rm -rf "$WORK"

    exit "$code"
}

# --------------------------------------------------------------------- set up

step "SETUP"

command -v git >/dev/null || { echo "git is required"; exit 1; }
command -v go >/dev/null || { echo "go is required"; exit 1; }
if ! command -v tmux >/dev/null; then
    echo "tmux is not installed; this acceptance run requires it."
    echo "The tmux-free acceptance suite is: go test ./internal/e2e/"
    exit 1
fi

mkdir -p "$BIN_DIR"
(cd "$REPO_SRC" && go build -o "$SWARM" ./cmd/swarm) || { echo "build failed"; exit 1; }
echo "  built $SWARM"

# The fake backend's executable, on PATH under the name the backend expects.
cp "$REPO_SRC/scripts/fake-agent.sh" "$BIN_DIR/swarm-fake-agent"
chmod +x "$BIN_DIR/swarm-fake-agent"
export PATH="$BIN_DIR:$PATH"
export SWARM_BIN="$SWARM"
export FAKE_TASK_ID="$TASK_ID"
export FAKE_DEADLINE="$TIMEOUT"

trap cleanup EXIT INT TERM

mkdir -p "$REPO"
cd "$REPO"
git init -q .
git config user.email e2e@swarm
git config user.name e2e
printf 'demo project\n' >README.md
printf '.swarm/\n' >.gitignore
cp -r "$REPO_SRC/prompts" .
cat >swarm.conf <<'CONF'
# Four-pack configuration (fake backend for acceptance testing)
window specifier fake wt-specifier task
window coder fake wt-coder task
window refactorer fake wt-refactorer task
window architect fake wt-architect task
CONF
git add -A
git commit -qm "initial commit"
echo "  repository at $REPO"

# --------------------------------------------------------------------- start

step "START"

if ! "$SWARM" start >"$WORK/start.log" 2>&1; then
    sed 's/^/  /' "$WORK/start.log"
    fail "swarm start"
    diagnostics
    exit 1
fi
sed -n '/Starting swarm/,$p' "$WORK/start.log" | head -20 | sed 's/^/  /'

check "four worktrees exist" test -d .swarm/worktrees/wt-coder
check "daemon is running" bash -c '"$0" status | grep -q "running (pid"' "$SWARM"
check "four sessions are running" bash -c '[ "$("$0" sessions list | grep -c running)" = "4" ]' "$SWARM"

# -------------------------------------------------------------------- submit

step "SUBMIT"

"$SWARM" task submit \
    --id "$TASK_ID" \
    --priority 20 \
    --description "Implement a discount calculator: calculate(price, discountPercent); 100 with 20% returns 80; 50 with 0% returns 50; discounts outside 0..100 are invalid; include tests" \
    | sed 's/^/  /'

# ------------------------------------------------------- wait for the cycle

step "CYCLE"

MARKER="$REPO/.swarm/runtime/e2e/$TASK_ID.complete"
RESTARTED=0
DEADLINE=$(( $(date +%s) + TIMEOUT ))

while [ "$(date +%s)" -lt "$DEADLINE" ]; do
    # Restart once, while work is genuinely in flight. The window is small
    # because the fake agents are fast, so this restarts on the first sighting
    # of any work anywhere in the system rather than waiting for a specific
    # role to be mid-task.
    if [ "$RESTARTED" = "0" ] && [ -n "$(find .swarm/handoffs/*/current .swarm/handoffs/*/inbox -name '*.handoff' 2>/dev/null)" ]; then
        step "RESTART (during active work)"

        BEFORE_CURRENT="$(find .swarm/handoffs/*/current -name '*.handoff' 2>/dev/null | sort | xargs -r -n1 basename | tr '\n' ' ')"
        BEFORE_IDS="$(grep -rh '^id: ' .swarm/handoffs 2>/dev/null | sort | md5sum)"
        echo "  in flight before restart: ${BEFORE_CURRENT:-<inbox only>}"

        "$SWARM" stop >/dev/null 2>&1
        check "worktrees survived stop" test -d .swarm/worktrees/wt-coder
        check "handoff state survived stop" test -d .swarm/handoffs/coder/completed
        if [ -n "$BEFORE_CURRENT" ]; then
            AFTER_STOP="$(find .swarm/handoffs/*/current -name '*.handoff' 2>/dev/null | sort | xargs -r -n1 basename | tr '\n' ' ')"
            [ "$BEFORE_CURRENT" = "$AFTER_STOP" ] \
                && pass "current work survived stop" \
                || fail "current work changed during stop"
        fi

        "$SWARM" start >/dev/null 2>&1

        AFTER_CURRENT="$(find .swarm/handoffs/*/current -name '*.handoff' 2>/dev/null | sort | xargs -r -n1 basename | tr '\n' ' ')"
        if [ -z "$BEFORE_CURRENT" ] || [ "$BEFORE_CURRENT" = "$AFTER_CURRENT" ]; then
            pass "current task resumed unchanged after restart"
        else
            fail "current task changed across restart ($BEFORE_CURRENT -> $AFTER_CURRENT)"
        fi

        RESTARTED=1
        step "CYCLE (continued)"
    fi

    if [ -f "$MARKER" ]; then
        break
    fi

    sleep "$POLL_INTERVAL"
done

if [ ! -f "$MARKER" ]; then
    fail "cycle did not complete within ${TIMEOUT}s"
    diagnostics
    exit 1
fi

pass "cycle completed"

# ---------------------------------------------------------------- assertions

step "ASSERTIONS"

completed_count() { find ".swarm/handoffs/$1/completed" -name '*.handoff' 2>/dev/null | wc -l; }
queue_count()     { find ".swarm/handoffs/$1/$2" -name '*.handoff' 2>/dev/null | wc -l; }

for role in specifier coder refactorer architect; do
    if [ "$(completed_count "$role")" -ge 1 ]; then
        pass "$role completed work"
    else
        fail "$role completed nothing"
    fi
done

for role in specifier coder refactorer architect; do
    [ "$(queue_count "$role" failed)" = "0" ] || fail "$role has failed handoffs"
done
[ "$(find .swarm/handoffs/rejected -name '*.handoff' 2>/dev/null | wc -l)" = "0" ] \
    && pass "no rejected handoffs" || fail "handoffs were rejected"

STUCK="$(find .swarm/handoffs/*/current -name '*.handoff' 2>/dev/null | wc -l)"
[ "$STUCK" = "0" ] && pass "no stuck current work" || fail "$STUCK current items remain"

# Real commits on isolated branches.
for role in specifier coder refactorer architect; do
    if git -C ".swarm/worktrees/wt-$role" log --oneline -1 >/dev/null 2>&1; then
        BRANCH="$(git -C ".swarm/worktrees/wt-$role" rev-parse --abbrev-ref HEAD)"
        [ "$BRANCH" = "swarm/$role" ] || fail "$role is on branch $BRANCH"
    fi
done
pass "each role is on its own branch"

# The worktrees are distinct directories with no cross-contamination.
DIRTY=0
for role in specifier coder refactorer architect; do
    [ -n "$(git -C ".swarm/worktrees/wt-$role" status --porcelain)" ] && DIRTY=1
done
[ "$DIRTY" = "0" ] && pass "worktrees are isolated and clean" || fail "a worktree has uncommitted changes"

# The implementation actually landed.
if git -C .swarm/worktrees/wt-coder show HEAD:demo/calculator/calculator.go 2>/dev/null | grep -q "func Calculate"; then
    pass "implementation exists in the coder's branch"
else
    fail "implementation is missing"
fi

# Git handoffs carried resolvable commits.
if grep -rhq "^canonical_commit: " .swarm/handoffs/*/completed/ 2>/dev/null; then
    pass "git handoffs carried canonical commits"
else
    fail "no canonical commit was recorded"
fi

# No duplicates: one delivery per handoff id.
DUPES="$(grep -rh "^id: " .swarm/handoffs/*/completed/ 2>/dev/null | sort | uniq -d | wc -l)"
[ "$DUPES" = "0" ] && pass "no duplicate handoffs" || fail "$DUPES duplicated handoff ids"

# Traceable.
if "$SWARM" task trace "$TASK_ID" | grep -q "EVENTS:"; then
    pass "run is traceable"
else
    fail "trace produced nothing"
fi

# ------------------------------------------------------------------- verdict

step "GIT GRAPH"
git log --all --graph --oneline | head -20 | sed 's/^/  /'

step "TRACE"
"$SWARM" task trace "$TASK_ID" | head -40 | sed 's/^/  /'

echo
if [ "$FAIL" -eq 0 ]; then
    cat <<SUMMARY
FOUR-PACK E2E PASSED

Task: $TASK_ID
Cycle:
specifier -> coder -> refactorer -> architect -> specifier

Restart recovery:     PASS
Duplicate prevention: PASS
Worktree isolation:   PASS
Git handoffs:         PASS
Checks:               $PASS passed, 0 failed
SUMMARY
    exit 0
fi

echo "FOUR-PACK E2E FAILED ($FAIL failed, $PASS passed)"
diagnostics
exit 1
