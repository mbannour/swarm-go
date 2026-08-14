# Four-pack parity

Where this Go implementation stands against the SwarmForge four-pack workflow it
reimplements.

Every claim names the evidence, and evidence has three strengths:

| Level | Meaning |
|---|---|
| **UNIT** | proven by a unit test against the real component |
| **FAKE E2E** | proven end-to-end with deterministic fake agents, no model |
| **REAL** | proven with a real Codex agent, unattended |

A box is only ticked when a named test proves it. Anything unproven is a gap,
however finished the code looks.

```bash
./scripts/parity.sh                              # deterministic gate: build, vet, test, -race, fake E2E
RUN_REAL_CODEX_TESTS=1 ./scripts/real-codex-smoke.sh   # opt-in, costs model quota
```

## Categories

### Configuration
| | Feature | Evidence |
|---|---|---|
| ✓ | Four roles, worktrees, receive modes from `swarm.conf` | UNIT `internal/config` |
| ✓ | Approval policy field, defaulting to interactive | UNIT `TestApprovalPolicyDefaultsToInteractive`, `TestApprovalPolicyIsParsed` |
| ✓ | Unknown policy rejected | UNIT `TestUnknownApprovalPolicyIsRejected` |

### Worktrees
| | Feature | Evidence |
|---|---|---|
| ✓ | One isolated worktree and branch per role | UNIT `internal/git`; FAKE E2E `assertWorktreeIsolation` |
| ✓ | Repo root resolves from inside a worktree | UNIT `TestRepoRootFromLinkedWorktree` |
| ✓ | Safe removal and pruning, never `rm -rf` | UNIT `internal/git` |

### tmux
| | Feature | Evidence |
|---|---|---|
| ✓ | Project-isolated socket, session per role | UNIT `internal/tmux` |
| ✓ | Wake-up is submitted, not merely typed | UNIT `TestSendPromptSubmitsTheLine` (**regression B**) |
| ✓ | Two independent repositories side by side | UNIT `TestIntegrationTwoRepositoriesAreIndependent` |

### Prompts
| | Feature | Evidence |
|---|---|---|
| ✓ | Constitution + runtime protocol + role + generated context | UNIT `internal/prompt` |
| ✓ | Stable `SWARM_BIN` path, temp builds refused | UNIT `binary_test.go` |
| ✓ | Agents obey the protocol unprompted | **REAL** — observed ready → work → next → done |

### Handoffs
| | Feature | Evidence |
|---|---|---|
| ✓ | Durable files, daemon delivery, priority ordering | UNIT `internal/handoff` |
| ✓ | `git_handoff` with a resolved 10-character commit | UNIT + FAKE E2E |
| ✓ | Idempotent delivery, no duplicates after a crash | UNIT `TestCrashBetweenSendAndDone` |
| ✓ | Well-formed `git_handoff` produced by a real agent | **REAL** — coder → refactorer |

### Receiver lifecycle
| | Feature | Evidence |
|---|---|---|
| ✓ | ready / current / done, task and batch modes | UNIT `internal/handoff/lifecycle_test.go` |
| ✓ | Restart resumes current work | UNIT `TestRestartDuringActiveWork` |
| ✓ | Send-before-done keeps work active on failure | UNIT `TestFailedSendLeavesWorkActive` |

### Git integration
| | Feature | Evidence |
|---|---|---|
| ✓ | Fast-forward, cherry-pick, conflict abort | UNIT `internal/git/integrate_test.go` |
| ✓ | Idempotent by patch equivalence | UNIT `TestCherryPickIsIdempotent` |
| ✓ | Dirty worktree and wrong branch refused | UNIT `TestIntegrateRefusesDirtyWorktree` |
| ✓ | Every handoff integrated before downstream work | FAKE E2E `assertIntegrationHappened` |
| ✓ | Real agent commits real, passing code | **REAL** — tests passed in the coder's worktree |

### Lifecycle
| | Feature | Evidence |
|---|---|---|
| ✓ | Ordered start/stop, idempotent, partial startup reported | UNIT `internal/lifecycle` |
| ✓ | `stop` preserves worktrees and handoffs | UNIT `TestStopPreservesDurableState` |
| ✓ | Machine-readable status | UNIT `TestStatusJSON` |

### Recovery
| | Feature | Evidence |
|---|---|---|
| ✓ | Typed diagnostics, safe repair, dry run | UNIT `internal/diagnostics`, `internal/repair` |
| ✓ | Daemon/session/agent/socket/PID/orphan recovery | UNIT `internal/lifecycle/recovery_test.go` |
| ✓ | Dirty worktrees never touched by repair | UNIT `TestRepairNeverTouchesDirtyWorktree` |

### Notification reliability
| | Feature | Evidence |
|---|---|---|
| ✓ | One notification path for delivery, submit and reconcile | UNIT `internal/notify` |
| ✓ | `task submit` notifies the entry role | UNIT `TestRegressionA_InboxWorkWithoutDeliveryStillNotifies` (**regression A**); **REAL** — `NOTIFIED: yes`, specifier woke |
| ✓ | Delivery and notification are separate states | UNIT `internal/notify` |
| ✓ | Bounded retry, capped attempts, no flood | UNIT `TestShouldRetryWaitsForTheInterval`, `TestRetriesAreCapped` |
| ✓ | Reconciliation catches an unheard wake-up | UNIT `TestReconcileIgnoresRolesThatAreWorking` |
| ✓ | Notification state in `status` and `--json` | UNIT `internal/lifecycle` |

### Backend autonomy
| | Feature | Evidence |
|---|---|---|
| ✓ | interactive / autonomous / restricted policies | UNIT `internal/agent/regression_test.go` |
| ✓ | Autonomous launch carries validated flags | UNIT `TestRegressionC_AutonomousLaunchCarriesApprovalFlags` (**regression C**) |
| ✓ | Unsupported policy fails loudly | UNIT `TestUnsupportedPolicyIsAnError` |
| ✓ | Backend capability model | UNIT `TestCapabilities` |
| ✓ | Agent commits without an approval prompt | **REAL** — see the smoke test |

### Backend bootstrap
| | Feature | Evidence |
|---|---|---|
| ✓ | Workspace trust detected, unattended start refused until ready | UNIT `TestReadinessBlocksOnUntrustedWorkspace` |
| ✓ | `swarm bootstrap` records trust via Codex's own config | **REAL** — smoke test bootstrap step |
| ✓ | Interactive roles may still meet the prompt | UNIT `TestReadinessAllowsInteractiveWithoutTrust` |

### Fake-agent E2E
| | Feature | Evidence |
|---|---|---|
| ✓ | Full cycle, developer → specifier → … → specifier | FAKE E2E `TestFourPackEndToEnd`, `scripts/e2e-fourpack.sh` |
| ✓ | Restart mid-flight, duplicate prevention, isolation | FAKE E2E |

### Real-agent smoke
| | Feature | Evidence |
|---|---|---|
| ✓ | Unattended submit → specifier → coder → refactorer | **REAL** `scripts/real-codex-smoke.sh` — 10/10 checks, 125s |
| ✓ | No manual tmux typing, Enter or approval | **REAL** — the script fails if a wait times out |
| ✓ | Real agent writes code, tests pass, commits | **REAL** — commit contains Go source, `go test` passes |
| ✓ | Real agent produces a valid `git_handoff` downstream | **REAL** — refactorer received it |
| ✓ | Sandboxed toolchain caches reachable | **REAL** — `writable` roots via `--add-dir` |
| ✓ | Unattended commits need `trusted` | **REAL** — `workspace-write` keeps `.git` read-only |

### Real four-pack E2E
| | Feature | Evidence |
|---|---|---|
| ✓ | Full real cycle: specifier → coder → refactorer → architect → specifier | **REAL** `scripts/real-fourpack-e2e.sh` — 20/20 checks, 291s |
| ✓ | Every hop unattended, no manual input anywhere | **REAL** — each hop is a bounded wait that fails on stall |
| ✓ | Handed-off commit integrated by the receiver | **REAL** — `METHOD: fast-forward` in the trace |
| ✓ | Each role does its own job, not another's | **REAL** — refactorer declined to refactor, architect declined to code |
| ✓ | Clean queues, real commits, implementation in history | **REAL** — assertions in the script |

## Gaps

Ordered roughly by how much they matter.

| | Gap | Notes |
|---|---|---|
| ☐ | **Final merge to the base branch is manual** | Role branches converge role-to-role, and the accepted result ends on the specifier's branch. Nothing moves it to `main`: `swarm task merge` was deliberately not built, so releasing stays a human step. Merge it yourself with ordinary Git. |
| ☐ | **Multi-commit handoffs are not supported** | A `git_handoff` names exactly one commit. A range would be a separate, explicit feature. |
| ☐ | **Conflicts need a human** | By design: the cherry-pick aborts, `doctor` reports `INTEGRATION_FAILED`, and repair never resolves it. |
| ✓ | **Real-agent behavior: proven** | The full cycle runs unattended with real Codex (`scripts/real-fourpack-e2e.sh`). Five orchestrator bugs were found by running it and are now fixed and frozen as regression tests: `task submit` woke nobody; wake-ups were typed but never submitted; a linked worktree resolved the wrong repository root; sandboxed agents could not reach their toolchain caches; and `.git` was read-only under `workspace-write`, which is why unattended commits need the explicit `trusted` policy. |
| ☐ | **`done` does not enforce send-before-done** | The prompt and `handoff status` make the ordering visible, but an agent can still call `done` with `DOWNSTREAM_SENT: no` and drop the work. |
| ☐ | **Route is code, not configuration** | `internal/handoff/route.go` is the single source, but per-project routes are not configurable. SwarmForge's two-pack and six-pack shapes are therefore out of reach. |
| ☐ | **Batch handoffs collapse to one downstream message** | A batch's `source_handoff_id` is its first item's id, so a batch produces one downstream handoff rather than one per item. |
| ☐ | **Duplicate protection is per current-work** | A role that legitimately wants two different downstream messages from one task gets the first back from `handoff next`; `handoff send` is the escape hatch. |
| ☐ | **Agent liveness is a heuristic** | `pane_current_command` not being a shell counts as "running", so any foreground program reads as the agent — and `AGENT_MISSING` can therefore be a false positive. |
| ☐ | **Notification recovery is not automatic** | A delivered handoff whose tmux wake-up failed is not re-notified by repair; the recipient finds it with `handoff ready`. |
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
