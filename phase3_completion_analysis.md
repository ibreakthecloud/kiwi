# Phase 3 Completion Analysis — Startup BYOC Platform Pivot

**Date:** 2026-07-16
**Branch reviewed:** `feature/phase3-issue88` (up to date with `origin`)
**Reference docs:** [RFC: Startup-First BYOC Platform Pivot](docs/rfcs/2026-07-16-startup-byoc-platform-rfc.md), [Phased Plan](docs/PHASED_PLAN.md), [Architecture](docs/design/ARCHITECTURE.md), [Pivot Analysis](docs/PIVOT_ANALYSIS.md), [Startup Vision](docs/STARTUP_VISION.md)

---

## 1. Executive Summary

Phases 1–3 of the BYOC pivot are functionally implemented. The Data Plane (`kiwidaemon`) and Control Plane adaptations (lease queue, encrypted credentials, planner API) are solid and well-tested, having gone through PR review cycles (#100, #102, #103) with fixes applied. **Phase 3 (CLI, SDK, Linear webhook) is feature-complete but was committed directly to this branch without tests or review**, and contains one security gap (unauthenticated webhook), one functional defect (`kiwi claude` system-prompt injection doesn't work), and SDK limitations that block the RFC's headline CI/CD use cases. Documentation (README, CLAUDE.md) had drifted badly from the code; the README has been rewritten as part of this review.

**Verdict:** Phase 3 should not merge to `main` as-is. The follow-up items below (filed as GitHub issues) should be addressed first or immediately after merge.

---

## 2. Phase-by-Phase Status

### Phase 1 — Data Plane Foundation ✅ (issues #78–#81, closed)
| Deliverable | Location | Notes |
| :--- | :--- | :--- |
| Daemon scaffold | `cmd/kiwidaemon/main.go`, `pkg/daemon/` | Flags for API URL, key path, poll interval, cache dir; clean SIGINT/SIGTERM shutdown |
| Zero-knowledge crypto | `pkg/crypto/` (`keys.go`, `seal.go`, `symmetric.go`) | X25519 sealing + Ed25519 heartbeat signing, per RFC §4.1; has tests |
| Heartbeat polling | `pkg/daemon/client.go`, `daemon.go` | HTTPS pull model with jitter/backoff; has tests |
| Git worktree cache | `pkg/gitcache/cache.go` | Bare-clone + `git worktree add`; **unbounded** — LFU eviction deferred (issue #98) |
| Sandbox spawning | integrates `pkg/infra`, `pkg/sandbox` | Worktree→Docker mount path untested end-to-end (issue #99) |

### Phase 2 — Control Plane Adaptations ✅ (issues #82–#84, closed via PRs #100/#102/#103)
| Deliverable | Location | Notes |
| :--- | :--- | :--- |
| Event queue | `pkg/store/` (lease queue), `pkg/queue/` | Lease-based handoff with dead-lettering after `MaxLeaseAttempts` (review fix `8089f8a`) |
| Encrypted credential storage | `pkg/crypto/`, `pkg/store/` | X25519-sealed at rest; atomic upsert via `ON CONFLICT` (review fix `2940e35`) |
| Planner API | `pkg/planner/` (`handler.go`, `service.go`, `llm.go`, `heuristic.go`) | DAG decomposition + enqueue; manifest persist + enqueue in one transaction (review fix `af96284`) |

### Phase 3 — Integration Layer ⚠️ functionally complete, quality gaps (issues #85–#88, still OPEN)
| Deliverable | Commit | Files | Assessment |
| :--- | :--- | :--- | :--- |
| `kiwi` CLI (`login`, `submit`) | `e740108` | `cmd/kiwi/login.go`, `submit.go`, `main.go` | Works; token precedence flag → env → config; **no tests** |
| `kiwi claude` wrapper | `3386d0a` | `cmd/kiwi/claude.go` | **Defective**: sets `CLAUDE_SYSTEM_PROMPT` env var, which Claude Code does not read — the Swarm instructions are never injected |
| Node/Python SDKs | `2f13409` | `sdk/node/`, `sdk/python/` | Submit-only; no status polling/result download, so "self-healing CI" and "Sentry auto-triage" flows aren't achievable yet; **no tests, no README/docs** |
| Linear webhook | `8ef96dc` | `pkg/orchestrator/webhook.go` | **Unauthenticated** — no `Linear-Signature` HMAC verification; hardcoded `org_id: "default-org"` violates the multi-tenant constraint; **no tests** |

### Phase 4 — Distribution 🔜 (issues #89–#92, open)
Terraform/CloudFormation templates plus the lightweight dashboard trio (onboarding, God View grid, topology canvas). Not started.

---

## 3. Detailed Findings

### F1 — Linear webhook accepts unauthenticated requests (security, `pkg/orchestrator/webhook.go`)
`handleLinearWebhook` decodes the payload and creates a job with no verification that the request came from Linear. Linear signs webhooks with an HMAC-SHA256 `Linear-Signature` header over the raw body; without checking it, anyone who discovers the endpoint can enqueue arbitrary planner jobs (which consume LLM spend and touch repos). It also stamps every job `org_id: "default-org"` / `user_id: "linear-webhook"`, breaking the "every task-scoped row carries org_id" constraint from CLAUDE.md.

### F2 — `kiwi claude` prompt injection is a no-op (`cmd/kiwi/claude.go`)
The wrapper launches `claude` with a `CLAUDE_SYSTEM_PROMPT` environment variable. Claude Code does not honor that variable; the supported mechanism is the `--append-system-prompt` CLI flag. As shipped, `kiwi claude` is equivalent to running `claude` directly — the Swarm-offloading instructions never reach the model. Additionally, the flag set parses but discards all flags, and the hardcoded example in the prompt references the old flag-style submit invocation.

### F3 — Phase 3 shipped with zero tests
CLAUDE.md mandates "Every new feature must ship with tests first," and CI enforces `go test ./pkg/...`. Phases 1–2 comply (`pkg/crypto`, `pkg/daemon`, `pkg/gitcache`, `pkg/planner`, `pkg/queue`, `pkg/store` all have `_test.go` files). None of the Phase 3 surfaces do: `cmd/kiwi/*`, `pkg/orchestrator/webhook.go`, `sdk/node`, `sdk/python`. The webhook is in `pkg/`, so it is inside the CI test path but simply uncovered.

### F4 — SDKs cannot complete the RFC's flagship use cases
`sdk/node/index.js` and `sdk/python/kiwi/__init__.py` expose only `submitTask`/`submit_task`, returning the raw create response. The vision doc's headline integrations (failing CI triggers `kiwi.spawn()` → agent fixes → patch pushed; Sentry exception → draft PR) require status polling, log retrieval, and result download — all of which exist in the Go client (`pkg/client`) but not in either SDK. Neither SDK has docs, examples, or tests; the Node package (`kiwi-sdk`) declares `axios`/`form-data` deps but there is no lockfile or publish workflow.

### F5 — Documentation drift
- `README.md` (fixed during this review): linked to a deleted RFC (`docs/rfcs/2026-07-10-...`), described the abandoned enterprise architecture (Postgres/NATS/master-worker as the *target*), and showed the removed flag-style CLI invocation.
- `CLAUDE.md` (still stale): links to three nonexistent docs (`docs/rfcs/2026-07-10-agentic-execution-platform-rfc.md`, `docs/plans/2026-07-11-agentic-platform-implementation-plan.md`, `docs/plans/2026-07-11-deployment-runner-plan.md`) and its §4 run example uses the removed `./kiwi -token ... -task ...` form; the directory map omits `cmd/kiwidaemon`, `pkg/crypto`, `pkg/daemon`, `pkg/gitcache`, `pkg/planner`, `sdk/`.

### F6 — Working-tree hygiene (minor)
Untracked review artifacts sit at repo root: `pr100.diff`, `pr102.diff`, `pr103.diff`, `updated_pr*.diff`, `review_*.json`, plus an untracked `frontend/` directory. These should be removed or `.gitignore`d before the Phase 3 PR to avoid accidental commits.

---

## 4. Recommended Sequence

1. Fix F1 (webhook HMAC verification + org resolution) — security-sensitive, small change.
2. Fix F2 (`--append-system-prompt`) — one-line mechanism change plus prompt cleanup.
3. Add F3 tests (webhook handler is the priority since it's in CI's test path).
4. Open the Phase 3 PR to `main` with `Closes #85 #86 #87 #88`, README update included.
5. F4 (SDK polling) and F5 (CLAUDE.md refresh) as fast follow-ups.
6. Proceed to Phase 4 (issues #89–#92).
