#!/usr/bin/env bash
#
# A deterministic stand-in for a real coding agent.
#
# It is not a mock of the orchestrator: it drives exactly the runtime protocol
# the prompts give a real agent — ready, inspect, work, commit, next, done —
# through the same CLI, the same handoffs and the same tmux session. What it
# does not do is think; the repository changes it makes are fixed.
#
# Usage: swarm-fake-agent <role> <worktree> [prompt-file]
#
# Environment:
#   SWARM_BIN      path to the swarm executable (required)
#   FAKE_DEADLINE  seconds to keep working before exiting (default 120)
#   FAKE_POLL      seconds between polls when idle (default 1)

set -uo pipefail

ROLE="${1:?role required}"
WORKTREE="${2:?worktree required}"

SWARM_BIN="${SWARM_BIN:?SWARM_BIN must be set}"
DEADLINE="${FAKE_DEADLINE:-120}"
POLL="${FAKE_POLL:-1}"

cd "$WORKTREE" || exit 1

log() { printf '[fake-%s] %s\n' "$ROLE" "$*"; }

# value_of KEY <<< "$output" — reads one machine-readable field.
value_of() {
    local key="$1"
    sed -n "s/^${key}: //p" | head -1
}

commit_all() {
    local message="$1"

    if [ -z "$(git status --porcelain)" ]; then
        return 1              # nothing to commit
    fi

    git add -A
    git -c user.email=fake@swarm -c user.name="fake-$ROLE" commit -q -m "$message"

    return 0
}

# The role's deterministic contribution to the demo project.
do_work() {
    case "$ROLE" in
    specifier)
        mkdir -p demo/calculator
        cat >demo/calculator/SPEC.md <<'SPEC'
# Discount calculator

calculate(price, discountPercent) -> price after discount

Acceptance criteria:
- 100 with 20% returns 80
- 50 with 0% returns 50
- a discount below 0 is invalid
- a discount above 100 is invalid
SPEC
        commit_all "spec: discount calculator acceptance criteria"
        ;;

    coder)
        mkdir -p demo/calculator
        cat >demo/calculator/calculator.go <<'IMPL'
package calculator

import "errors"

// ErrInvalidDiscount is returned when a discount is outside 0..100.
var ErrInvalidDiscount = errors.New("discount must be between 0 and 100")

// Calculate applies discountPercent to price.
func Calculate(price float64, discountPercent float64) (float64, error) {
	if discountPercent < 0 || discountPercent > 100 {
		return 0, ErrInvalidDiscount
	}
	return price - (price * discountPercent / 100), nil
}
IMPL
        cat >demo/calculator/calculator_test.go <<'TEST'
package calculator

import "testing"

func TestCalculate(t *testing.T) {
	cases := []struct {
		price, discount, want float64
	}{
		{100, 20, 80},
		{50, 0, 50},
	}

	for _, c := range cases {
		got, err := Calculate(c.price, c.discount)
		if err != nil {
			t.Fatalf("Calculate(%v, %v): %v", c.price, c.discount, err)
		}
		if got != c.want {
			t.Errorf("Calculate(%v, %v) = %v, want %v", c.price, c.discount, got, c.want)
		}
	}
}

func TestCalculateRejectsInvalidDiscount(t *testing.T) {
	for _, discount := range []float64{-1, 101} {
		if _, err := Calculate(100, discount); err == nil {
			t.Errorf("discount %v was accepted", discount)
		}
	}
}
TEST
        # Verify before claiming completion, exactly as the constitution says.
        if command -v go >/dev/null 2>&1; then
            (cd demo/calculator && go test ./... >/dev/null 2>&1) || log "tests failed"
        fi
        commit_all "feat: discount calculator with tests"
        ;;

    refactorer)
        # The handed-off commit has been integrated by now, so the coder's
        # files really are here to refactor.
        mkdir -p demo/calculator
        if [ -f demo/calculator/calculator.go ]; then
            cat >demo/calculator/calculator.go <<'IMPL'
package calculator

import "errors"

// ErrInvalidDiscount is returned when a discount is outside 0..100.
var ErrInvalidDiscount = errors.New("discount must be between 0 and 100")

const (
	minDiscount = 0
	maxDiscount = 100
)

// Calculate applies discountPercent to price.
func Calculate(price float64, discountPercent float64) (float64, error) {
	if !validDiscount(discountPercent) {
		return 0, ErrInvalidDiscount
	}
	return price * (1 - discountPercent/maxDiscount), nil
}

// validDiscount reports whether a discount is within the accepted range.
func validDiscount(discountPercent float64) bool {
	return discountPercent >= minDiscount && discountPercent <= maxDiscount
}
IMPL
            if command -v go >/dev/null 2>&1; then
                (cd demo/calculator && go test ./... >/dev/null 2>&1) || log "tests failed after refactor"
            fi
        fi
        commit_all "refactor: extract discount validation"
        ;;

    architect)
        mkdir -p demo/calculator
        cat >demo/calculator/REVIEW.md <<'REVIEW'
# Architecture review

- Boundary: calculator is a pure function with no I/O. Good.
- Coupling: no dependencies beyond the standard library.
- Risk: money as float64 will accumulate rounding error; revisit if this
  moves anywhere near real currency handling.

Verdict: acceptable for the stated requirement.
REVIEW
        commit_all "docs: architecture review of the discount calculator"
        ;;
    esac
}

# One full pass of the worker protocol. Returns 0 when work was processed.
work_once() {
    local ready commit note
    ready="$("$SWARM_BIN" handoff ready "$ROLE" 2>&1)"

    if printf '%s' "$ready" | grep -q '^NO_TASK'; then
        return 1
    fi

    log "accepted $(printf '%s' "$ready" | value_of ID)"

    # A git_handoff names a commit: inspect it, then apply it to this
    # worktree. Skipping this would mean working on state we never received.
    commit="$(printf '%s' "$ready" | value_of CANONICAL_COMMIT)"
    if [ -n "$commit" ]; then
        git log -1 --oneline "$commit" >/dev/null 2>&1 && log "inspected commit $commit"

        integration="$("$SWARM_BIN" handoff integrate "$ROLE" 2>&1)"
        if printf '%s' "$integration" | grep -q '^INTEGRATION_CONFLICT'; then
            log "integration conflict; leaving work current for a human"
            printf '%s\n' "$integration"
            return 0
        fi
        log "integration: $(printf '%s' "$integration" | head -1) via $(printf '%s' "$integration" | value_of METHOD)"
    fi

    do_work
    note="$ROLE completed its part of the cycle"

    # The specifier closes the loop when the work comes back from the architect.
    if [ "$ROLE" = "specifier" ] && printf '%s' "$ready" | grep -q 'FROM: architect'; then
        log "cycle complete"
        mkdir -p "$(dirname "$COMPLETION_MARKER")"
        printf 'complete\n' >"$COMPLETION_MARKER"
        "$SWARM_BIN" handoff done "$ROLE" >/dev/null
        return 0
    fi

    # Send downstream BEFORE marking done: if this fails the work stays active.
    if head="$(git rev-parse --short=10 HEAD 2>/dev/null)" && [ -n "$head" ]; then
        "$SWARM_BIN" handoff next --from "$ROLE" \
            --type git_handoff \
            --task "$TASK_ID" \
            --commit "$head" \
            --priority 20 \
            --note "$note" >/dev/null || { log "handoff failed; keeping current work"; return 0; }
    else
        "$SWARM_BIN" handoff next --from "$ROLE" \
            --type note --priority 20 --note "$note" >/dev/null || return 0
    fi

    "$SWARM_BIN" handoff done "$ROLE" >/dev/null
    log "done"

    return 0
}

TASK_ID="${FAKE_TASK_ID:-DEMO-1}"
COMPLETION_MARKER="${FAKE_COMPLETION_MARKER:-$WORKTREE/../../runtime/e2e/$TASK_ID.complete}"

log "started in $WORKTREE"

# Bounded, not a busy loop: a real agent reacts to wake-ups, but a shell script
# has no way to receive one, so it polls with a hard deadline.
END=$(( $(date +%s) + DEADLINE ))
while [ "$(date +%s)" -lt "$END" ]; do
    work_once || sleep "$POLL"
done

log "deadline reached; exiting"
