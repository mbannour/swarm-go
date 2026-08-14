#!/usr/bin/env bash
#
# The deterministic parity gate. No AI service is involved, so this is safe to
# run in CI and on every commit.

set -uo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

FAIL=0
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=1; }

run() {
    local what="$1"; shift
    if "$@" >/tmp/parity-out.$$ 2>&1; then
        ok "$what"
    else
        bad "$what"
        tail -25 /tmp/parity-out.$$ | sed 's/^/      /'
    fi
    rm -f /tmp/parity-out.$$
}

step "BUILD"
run "go build ./..." go build ./...
run "go vet ./..." go vet ./...

step "TESTS"
run "go test ./..." go test ./...
run "go test -race ./..." go test -race ./...

step "FAKE-AGENT FOUR-PACK E2E"
if command -v tmux >/dev/null 2>&1; then
    run "scripts/e2e-fourpack.sh" ./scripts/e2e-fourpack.sh
else
    printf '  \033[33mSKIP\033[0m tmux not installed\n'
fi

step "RESULT"
if [ "$FAIL" -eq 0 ]; then
    echo "  PARITY SUITE PASSED"
    exit 0
fi

echo "  PARITY SUITE FAILED"
exit 1
