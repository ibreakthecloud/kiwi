# RFC: Free Tier via Per-Org Daemon Processes on Shared Hosts

**Date:** 2026-07-21
**Status:** Proposed
**Builds on:** [RFC: Managed Execution Tier](2026-07-17-managed-execution-tier-rfc.md) · [RFC: Startup-First BYOC Platform Pivot](2026-07-16-startup-byoc-platform-rfc.md)
**Related:** [Architecture Review](../design/2026-07-16-byoc-architecture-review.md) · [Free Tier TODO](../TODO_FREE_TIER.md)

## 1. Summary

Today a new signup cannot run anything: there is no fleet, and getting one is a manual, contact-us step. This RFC makes **every signup a `Free` org that can submit a task immediately**, executed on a **shared pool of hosts** that Kiwi operates. `Pro` graduates to a **dedicated fleet** (managed-dedicated or BYOC).

The central design decision — and the thing this RFC exists to pin down — is **how** free tenants share. The answer is:

> **Free tenants share hosts, not a daemon.** Each free org gets its own lightweight **daemon process** with its own keypair, packed many-to-a-host and scaled to zero when idle. It is *not* one multi-tenant daemon serving many orgs.

This choice is forced by the credential model, and it has the large payoff that **it requires no change to the credential, lease, or crypto code that already ships** — those are all per-daemon-keypair today, and a per-org daemon process is exactly one keypair for exactly one org.

## 2. Why per-org *process*, not a shared daemon, and not a per-org VM

### 2.1 A shared daemon breaks the credential model

Customer credentials are sealed to a **daemon's** X25519 public key: `SealCredentialsForDaemon(ctx, orgID, daemonPubKey)` (`pkg/store/credentials.go:74`) decrypts an org's whole vault and re-seals it to one daemon key; the daemon opens it in memory with `crypto.OpenSealed` (`pkg/daemon/daemon.go:296`). A daemon that served many orgs would therefore hold the private key that can open **every** served org's credentials — a single-key, all-tenants blast radius. The Managed RFC §3.2 already names this: *"A shared multi-tenant daemon pool is tempting and wrong."*

Because each free org already brings its own model key (BYOK — the org stores e.g. `ANTHROPIC_API_KEY` as a credential; there is no Kiwi-provided model credit), the shared secret this would expose is **real and per-user**, not hypothetical.

### 2.2 A per-org VM does not scale to a free tier

The Managed RFC §3.2.1 provisions **one always-on VM per org, hand-provisioned, no autoscaler** — correct at ~10 paying customers, absurd at thousands of mostly-idle free orgs. The warm-git-cache argument that justifies always-on VMs (§3.2.1) matters far less for free, where a few seconds of cold start is acceptable.

### 2.3 A per-org *process* keeps the credential model and scales

A daemon is a Go process that persists a keypair and a bare git cache on local disk. Nothing requires it to own a whole VM. So:

- **One process = one keypair = one org.** `SealCredentialsForDaemon` / `OpenSealed` / `LeaseNextTask(orgID, …)` are used **unchanged**. The blast radius of a compromised process is one org, enforced by the crypto that already ships.
- **Many processes per host.** Density comes from packing processes, not from multi-tenanting a daemon.
- **Scale to zero.** A free org with no work has no running process. Cold start on submit is *process launch + `gitcache` clone* (seconds), not *VM boot* (30–60s).

The isolation boundary that used to be "the VM" moves to **two cheap layers** (§4).

## 3. Architecture

```
                 Control Plane (Kiwi SaaS) — already built
   ┌──────────────────────────────────────────────────────────────┐
   │ signup → Organization{Plan:"free"}   (pkg/store/models.go:14) │
   │ ActivateOrg → ProvisioningRequest{Type:"provision"}           │
   │              (pkg/auth/activation.go:12 — ENQUEUES today)     │
   │ /api/v1/daemon/join-token   (server.go:408 — exists)         │
   │ /api/v1/daemon/register     (server.go:451 — exists)         │
   │ /api/v1/daemon/heartbeat    (server.go:452 — exists, leases  │
   │                              + seals per org)                 │
   └───────────────┬──────────────────────────────────────────────┘
                   │  NEW: Provisioner consumes ProvisioningRequest
                   ▼
   ┌──────────────────────────────────────────────────────────────┐
   │  SHARED FREE HOST POOL  (Kiwi-operated)      NEW              │
   │  ┌────────────────────┐  ┌────────────────────┐              │
   │  │ daemon proc org A  │  │ daemon proc org B  │  ← container  │
   │  │ own keypair        │  │ own keypair        │    per org    │
   │  │ leases org A only  │  │ leases org B only  │               │
   │  │  ▼ Actor/Critic    │  │  ▼ Actor/Critic    │  (holds key,  │
   │  │    (daemon proc)   │  │    (daemon proc)   │   runs LLM)   │
   │  │  ▼ test cmd        │  │  ▼ test cmd        │               │
   │  │   [gVisor sandbox] │  │   [gVisor sandbox] │  ← NEW: runsc │
   │  │   NetworkNone      │  │   NetworkNone      │   on the box  │
   │  └────────────────────┘  └────────────────────┘   that runs  │
   │  scale-to-zero when idle · cold-start on submit   model code  │
   └──────────────────────────────────────────────────────────────┘
```

The daemon **binary, heartbeat, lease, seal, and execution paths are unchanged.** The only new components are (a) a **Provisioner** that consumes `ProvisioningRequest` and manages per-org daemon-process lifecycle on the pool, and (b) a **gVisor sandbox path** for the test command. Everything else is configuration and limits.

## 4. Isolation: two layers, both cheap

The LLM loop already runs **in the daemon process, not the sandbox**, and LLM keys are **withheld from the sandbox** (`isLLMKey`, `pkg/daemon/daemon.go:307-320,394-398`). Only the test command runs in the sandbox, already with `NetworkNone: true` (`daemon.go:389`). So the untrusted surface is small and already partly contained. On shared hosts we add:

1. **Per-org daemon process in its own container/namespace.** Protects one org's key + worktree from the next. This is Kiwi's Go code holding one org's secret — a standard container is defensible here.
2. **Test-command sandbox in a microVM-grade boundary (gVisor `runsc`).** This is the only place hostile, model-generated code runs. gVisor is a one-flag change on the existing Docker driver (`--runtime=runsc`, `pkg/sandbox/exec.go:95`) — no new runtime to build. Graduate to Firecracker/Kata or a rented sandbox (E2B/Modal) if escape-into-our-network blast radius demands it. **Do not build on `pkg/sandbox/firecracker.go`** — it is a `sudo`/`dd`/`mkfs` prototype (`firecracker.go:29-52`), not a path.

## 5. Credentials & the one honest residue

- **Model key (BYOK):** stored per-org, encrypted at rest (`crypto.EncryptAtRest`), sealed per-heartbeat to that org's **own** daemon key, opened in that daemon process's memory. Blast radius: one org.
- **Honest residue:** because the loop runs in the daemon on Kiwi's host, **Kiwi's host holds the user's model key in process memory for the duration of a free job.** The per-org keypair bounds this to one org — this is the same trust posture as E2B/Modal. The *only* way to hold zero model secrets on Kiwi hosts is a **CP-side LLM gateway** (daemon's provider `base_url` → a Control-Plane proxy that injects the key, so it never leaves the CP). That is deferred as optional hardening (Phase 5), not required to ship.
- **VCS:** free uses the existing `creds["GIT_TOKEN"]` path (`daemon.go:503`) for v1. Hardening to a **GitHub App** minting short-lived per-repo tokens is Phase 5.

## 6. Economics & abuse

BYOK changes the cost shape decisively: **inference is on the user's bill**, so a free user's runaway loop burns *their* quota, not Kiwi's. Token-cost runaway is self-limiting. Kiwi's free COGS is **compute only**, which makes the tier viable.

The remaining abuse vector is therefore **CPU** (cryptomining), not tokens. Defenses: the sandbox is already `NetworkNone`; add a hard per-task CPU/wall-clock cap and treat "CPU burning with no model calls" as an abuse signal. Free limits (`store.OrgLimits`) cap concurrency and per-job budget; the queue already enforces both (`pkg/store/queue.go:97-119,147-175`).

> **Note — `OrgLimits` drift.** Two structs exist: `store.OrgLimits` (enforced by the lease queue) and `auth.OrgLimits` (`pkg/auth/limits.go`, different fields, enforced against `task_states`). Free tier must set the **store** one. Reconciling the two is scoped into Phase 2.

## 7. Tiering

| | Free | Pro (managed-dedicated) | Pro (BYOC) |
| :--- | :--- | :--- | :--- |
| Compute | shared pool | dedicated VM(s) | customer VPC |
| Daemon | per-org process, scale-to-zero | per-org warm VM | customer-operated |
| Fleet | `fleet_id` = shared-free routing | dedicated `fleet_id` | dedicated `fleet_id` |
| Credentials | sealed to per-org process key | sealed to dedicated daemon | never leaves VPC |
| Model key on Kiwi host | yes, transient, one-org blast radius | yes | no |
| Isolation | gVisor on test sandbox | gVisor/Firecracker | customer's choice |
| Cost to Kiwi | compute only | compute | orchestration only |

The daemon does not know its tier. Tier is `Organization.Plan` + which fleet its work is pinned to — data, not a code fork. This preserves the Managed RFC §3.1 invariant ("one daemon, two operators").

## 8. Phased plan

Full detail, per-phase context, file references, and acceptance criteria live in **[docs/TODO_FREE_TIER.md](../TODO_FREE_TIER.md)**. Summary:

- **Phase 0 — Verify the seam (mostly done).** Confirm register→heartbeat→lease→seal→execute→PR runs end-to-end against a real Postgres + a locally-launched daemon. The code exists; this is a verification gate, not new work.
- **Phase 1 — Free provisioner + per-org process lifecycle.** Consume `ProvisioningRequest`; mint a join token and launch/stop a per-org daemon **container** on the pool; scale-to-zero + cold-start-on-submit. *The core gap.*
- **Phase 2 — Tier wiring + free limits.** On signup set the free `store.OrgLimits` profile and pin free work to the shared-free fleet; gate submit by plan; reconcile the `OrgLimits` drift.
- **Phase 3 — gVisor isolation.** Wire `runsc` behind a sandbox config flag; free hosts default to it; Docker remains the fallback.
- **Phase 4 — Abuse controls + metering.** Per-task CPU/wall-clock caps; cryptomining signal; agent-minutes metering and free-ceiling enforcement.
- **Phase 5 — Optional hardening.** CP-side LLM gateway (zero model secrets on Kiwi hosts); GitHub App JIT VCS tokens.

## 9. Drawbacks & alternatives

- **Cold start on free submit.** Accepted: seconds (process + clone), and only on the first task after idle. Warm processes are a Pro feature.
- **Model key transiently on Kiwi hosts (§5).** Mitigated by per-org keypair; eliminated only by the Phase 5 gateway. Scoped to free/managed; never wired into BYOC.
- **Alternative: literally shared daemon + cross-org lease (`LeaseNextFleetTask`).** Rejected — it collapses the credential model (§2.1). The per-org-process model needs no queue change at all.
- **Alternative: per-org VM (Managed RFC §3.2.1).** Rejected for free — does not scale to thousands of idle orgs (§2.2). It remains the Pro path.

## 10. Open questions

1. **Pool orchestration substrate.** Are per-org daemon containers scheduled on plain Docker hosts, Nomad, or k8s? Phase 1 should pick the smallest thing that supports launch/stop/scale-to-zero.
2. **Idle reclaim policy.** After how long idle does a free daemon process get reclaimed, and does its warm git cache survive reclaim (local disk) or not?
3. **Fair scheduling under contention.** With many per-org processes on a host, what bounds one org starving others — cgroup shares per process, or a scheduler-level cap?
4. **Free ceiling unit.** Agent-minutes vs. job-count vs. wall-clock. Phase 4 must pick one and meter it.
