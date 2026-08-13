# Four-pack parity

Where this Go implementation stands against the SwarmForge four-pack workflow it
reimplements.

A box is only ticked when a test proves it. "Proven by" names the test or script
that would fail if the behavior regressed. Anything unproven is listed as a gap,
even where the code looks finished.

Run everything below with:

```bash
go test ./...            # everything except the tmux-level acceptance run
./scripts/e2e-fourpack.sh   # the full CLI cycle, requires tmux
```

## Proven

| | Feature | Proven by |
|---|---|---|
| ✓ | Four roles from configuration | `config` tests, `TestValidateFourPack*` |
| ✓ | `swarm.conf` parsing and validation | `internal/config` |
| ✓ | Role prompts, shared constitution | `internal/prompt` |
| ✓ | Shared runtime protocol in every prompt | `TestAssembleIncludesEverySection`, `TestShippedPromptsAssemble` |
| ✓ | Stable agent binary path (`SWARM_BIN`) | `TestAssembleLifecycleCommandsUseTheBinary`, `binary_test.go` |
| ✓ | Git worktree per role | `internal/git`, `TestWorktreeLifecycle` |
| ✓ | Deterministic branches `swarm/<role>` | `TestBranchName`, e2e `assertWorktreeIsolation` |
| ✓ | Worktree isolation (separate branches, no cross-contamination) | `assertWorktreeIsolation`, `e2e-fourpack.sh` |
| ✓ | Project-isolated tmux socket | `TestSocketPath`, `TestDaemonLocksAreRepositoryScoped` |
| ✓ | tmux session per role, started in its worktree | `TestSessionLifecycle` |
| ✓ | Agent launch inside the session | `internal/agent`, `e2e-fourpack.sh` |
| ✓ | Backend abstraction (codex, fake) | `TestLookup`, `TestCodexCommand` |
| ✓ | Durable handoffs on disk | `internal/handoff` store tests |
| ✓ | Handoff daemon with delivery | `TestScanDeliversAndCanonicalisesCommit` |
| ✓ | Single daemon per repository (real lock) | `TestDaemonCannotStartTwice` |
| ✓ | `note` handoffs | `TestUnmarshalNote`, e2e |
| ✓ | `git_handoff` with a 10-character commit | `TestValidateCommitLength` |
| ✓ | Commit resolution against the project repository | `TestResolveCommit*`, `TestResolveCommitIsRepositoryScoped` |
| ✓ | Canonical commit delivered to the receiver | `TestScanDeliversAndCanonicalisesCommit`, e2e |
| ✓ | Priority ordering, oldest-first ties | `TestSelectTask*`, `TestListOrdersByPriorityThenAge` |
| ✓ | Task receive mode | `TestReadyTaskModeMovesOneItemToCurrent` |
| ✓ | Batch receive mode | `TestReadyBatchModeTakesEveryTopPriorityItem`, `TestBatchRoleTakesEveryTopPriorityItem` |
| ✓ | `ready` / `current` / `done` lifecycle | `internal/handoff/lifecycle_test.go` |
| ✓ | inbox / outbox / sent / failed / rejected / current / completed | `TestEnsureDirsCreatesEveryBox`, `TestRejectAndFailAreDistinct` |
| ✓ | Four-pack routing | `TestNextRole`, `TestRouteIsAClosedCycle` |
| ✓ | Idempotent downstream handoff (`handoff next`) | `TestAdvanceIsIdempotentForTheSameWork` |
| ✓ | Duplicate prevention across a crash | `TestCrashBetweenSendAndDone` (unit + e2e) |
| ✓ | Restart recovery of current work | `TestRestartDuringActiveWork`, `TestReadySurvivesRestart` |
| ✓ | Send-before-done semantics | `TestFailedSendLeavesWorkActive` |
| ✓ | Delivery idempotency by handoff id | `TestDeliverIsIdempotent` |
| ✓ | Notification failure never loses a handoff | `TestScanSurvivesNotifierFailure` |
| ✓ | Malformed input never stops the daemon | `TestScanRejectsMalformedFileAndKeepsGoing` |
| ✓ | External task boundary (`task submit`) | `TestFourPackEndToEnd` |
| ✓ | Run traceability from durable metadata | `assertTraceable`, `swarm task trace` |
| ✓ | `swarm start` / `status` / `stop` lifecycle | `internal/lifecycle` |
| ✓ | Ordered startup and shutdown | `TestStartOrder`, `TestStopOrder` |
| ✓ | Idempotent start and stop | `TestStartIsIdempotent`, `TestStopIsIdempotent` |
| ✓ | Partial startup reported, not pretended | `TestPartialStartupIsReportedAndFails` |
| ✓ | `stop` preserves worktrees and handoffs | `TestStopPreservesDurableState` |
| ✓ | Machine-readable status | `TestStatusJSON` |
| ✓ | Two repositories running independently | `TestRepositoriesAreIndependent`, `TestIntegrationTwoRepositoriesAreIndependent` |
| ✓ | Full cycle: developer → specifier → coder → refactorer → architect → specifier | `TestFourPackEndToEnd`, `./scripts/e2e-fourpack.sh` |

## Gaps

Ordered roughly by how much they matter.

| | Gap | Notes |
|---|---|---|
| ☐ | **Commit materialisation across worktrees** | A `git_handoff` transfers a commit's *identity*, not its content. The receiver's worktree is on its own branch and does not contain the sender's files until someone merges or cherry-picks. Nothing does that automatically today, by design — it is the next milestone. The e2e run shows this plainly: four sibling branches, no merges. |
| ☐ | **Real-agent behavior is unproven** | Every test uses deterministic fake agents. The orchestrator is proven; whether Codex reliably follows the runtime protocol is an empirical question no test here answers. |
| ☐ | **`done` does not enforce send-before-done** | The prompt and `handoff status` make the ordering visible, but an agent can still call `done` with `DOWNSTREAM_SENT: no` and drop the work. |
| ☐ | **Route is code, not configuration** | `internal/handoff/route.go` is the single source, but per-project routes are not configurable. SwarmForge's two-pack and six-pack shapes are therefore out of reach. |
| ☐ | **Batch handoffs collapse to one downstream message** | A batch's `source_handoff_id` is its first item's id, so a batch produces one downstream handoff rather than one per item. |
| ☐ | **Duplicate protection is per current-work** | A role that legitimately wants two different downstream messages from one task gets the first back from `handoff next`; `handoff send` is the escape hatch. |
| ☐ | **Agent liveness is a heuristic** | `pane_current_command` not being a shell counts as "running", so any foreground program reads as the agent. |
| ☐ | **`stop` does not drain in-flight handoffs** | Anything left in an outbox is delivered on the next start. |
| ☐ | **No `swarm clean`** | `swarm worktrees remove` is the manual path. |
| ☐ | **Agent logs are not captured** | Only the daemon writes a managed log; agent output lives in tmux scrollback. |
| ☐ | **Unix only** | flock, `setsid`, POSIX signals. No Windows implementation. |
| ☐ | **Single daemon assumed for delivery ordering** | Enforced by a lock, so it is safe — but there is no multi-daemon or multi-host story. |
| ☐ | **No quality gates, planner, or dynamic workflows** | Deliberately out of scope for parity v0.1. |

## What the acceptance run proves

`./scripts/e2e-fourpack.sh` builds the binary, creates a throwaway repository,
starts the real orchestrator with fake agents in real tmux sessions, and:

1. submits one requirement through the developer boundary;
2. watches it travel specifier → coder → refactorer → architect → specifier;
3. stops and starts the swarm mid-flight, asserting the in-flight work is
   unchanged;
4. asserts every role committed on its own branch, no queue holds failures or
   rejects, no current work is stuck, no handoff id is duplicated, and the
   implementation landed;
5. prints the Git graph and the derived trace.

On failure it dumps status, every queue, failure reasons, the daemon log, tmux
state, the Git graph and the trace before exiting non-zero.

## CI

`go test ./...` runs the whole suite except the tmux-level script. The
acceptance suite in `internal/e2e` is deliberately tmux-free — it uses the real
Git, handoff, daemon and lifecycle code with in-process fake agents — so the
main proof of correctness runs anywhere, and is never silently skipped.

`./scripts/e2e-fourpack.sh` needs `tmux` installed. Install it in CI to run the
full CLI path; it exits non-zero with diagnostics if anything regresses.
