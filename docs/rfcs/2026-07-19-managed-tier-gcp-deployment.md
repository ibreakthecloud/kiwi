# RFC: Managed Tier on GCP — Deployment & Operations

**Date:** 2026-07-19
**Status:** Proposed
**Realizes:** [RFC: Managed Execution Tier](2026-07-17-managed-execution-tier-rfc.md) Phases M1–M3 on Google Cloud
**Related:** [Execution & Isolation Model](2026-07-18-execution-and-isolation-model.md) · [Architecture Review](../design/2026-07-16-byoc-architecture-review.md)

## 1. Summary

The Managed Execution Tier RFC decided *what* managed is (Kiwi runs the daemon; one daemon per org; always-on, hand-provisioned in v1; microVM isolation before density; no zero-knowledge claim). This RFC decides *how we run it on GCP*, concretely enough to `terraform apply`.

It does **not** re-open any of those decisions. In particular it inherits, verbatim:

- **One always-on daemon per org, hand-provisioned. No autoscaler in v1** (Managed RFC §3.2.1). The daemon is a stateful pet with a warm git cache; scale-to-zero throws away the reason `pkg/gitcache` exists.
- **Managed is not zero-knowledge** (Managed RFC §4.1). Kiwi holds the X25519 private key and *can* decrypt. Do not write GCP docs, dashboards, or marketing that claim otherwise.
- **The escape-blast-radius problem is real from customer #1** (Managed RFC §4.2). A container escape in BYOC lands in the customer's account; the identical escape in managed lands in *our* GCP project. That asymmetry, not cross-tenant proximity, is what forces isolation.

The through-line: **the same `kiwidaemon` binary and the same lease/heartbeat protocol run on GCP.** Managed is an operator change, not an architecture change. This RFC is therefore mostly infrastructure — VPC, IAM, GCE, Cloud Run, Cloud SQL, Cloud KMS — plus three targeted code builds (KMS-backed key custody, a microVM sandbox driver, an egress proxy).

## 2. Target topology

```
                    ┌──────────────────── Kiwi GCP project ────────────────────┐
   users ──HTTPS──► │  Cloud Load Balancer (managed TLS)                        │
                    │        │                                                  │
                    │        ├─► Cloud Run: kiwid  -role api      (min=1, N≤)   │
                    │        └─► Cloud Run: frontend (Next.js)                  │
                    │                                                           │
                    │   Cloud Run: kiwid -role orchestrator  (min=max=1)        │
                    │   Cloud Run Job: kiwid -migrate  (pre-deploy, run-once)    │
                    │                                                           │
                    │   Cloud SQL (Postgres, private IP, HA, PITR)              │
                    │   Cloud KMS (encryption key)   Secret Manager (bootstrap) │
                    │   Artifact Registry            Cloud Logging/Trace/Monitor │
                    │                                                           │
                    │   ── daemon subnet (segmented, egress-controlled) ──      │
                    │   GCE VM  org A daemon   GCE VM  org B daemon   …          │
                    │     (hostile-by-default; Docker→Firecracker sandbox)      │
                    └───────────────────────────────────────────────────────────┘
                              daemon VMs reach ONLY: public API (LB) · model API · VCS
```

Two trust zones inside one project:

- **Control zone** — Cloud Run services + managed data stores. Trusted. Holds the DB, KMS key, provider/customer secrets.
- **Execution zone** — per-org GCE daemon VMs. **Treated as already hostile.** They run untrusted model-generated code. They get no ambient GCP credentials, can reach only the public API endpoint + allowlisted model/VCS egress, and cannot reach the control zone's private surface.

## 3. Control Plane on GCP

### 3.1 Compute — Cloud Run, split by role

`kiwid` already supports `-role api | orchestrator | all` and exposes `/healthz` (liveness) and `/readyz` (DB-checked readiness). Deploy the roles separately:

- **`kiwid -role api`** — Cloud Run service, `min-instances=1` (avoid cold starts on the paid entry tier), autoscale up on request load. Stateless; scales horizontally with no coordination.
- **`kiwid -role orchestrator`** — Cloud Run service, **`min=max=1`, CPU always allocated**. It runs background sweepers (`RequeueExpiredLeases` and future reconcilers). Until those are proven idempotent under concurrency (§7.2), it must be a **singleton** — a second replica would double-sweep.
- **Frontend** — Next.js on its own Cloud Run service (SSR) or built static → Cloud CDN. `NEXT_PUBLIC_KIWI_API_URL` points at the API's public URL.

Drop Caddy (the compose-era TLS terminator). Cloud Load Balancing + a Google-managed certificate front the API and frontend.

### 3.2 Migrations — a run-once Job, not on the serving path

Today `RunMigrations` executes on process boot. With `api` autoscaling to N replicas, N boots race on the same schema. Extract a **`kiwid -migrate` mode** that runs migrations and exits, wire it as a **Cloud Run Job** executed once in the deploy pipeline *before* new revisions receive traffic. Serving processes then assume the schema is current (and fail `/readyz` if a required table is absent).

### 3.3 Data — Cloud SQL for PostgreSQL

- Private IP only; reached from Cloud Run via the **Serverless VPC Access connector** (or the Cloud SQL Go connector). No public IP.
- HA (regional) + automated backups + PITR. This is the single source of truth (jobs, lease queue, sealed credentials, orgs).
- Connection pooling: Cloud Run fan-out can exhaust Postgres connections; cap per-instance pool size and/or front with PgBouncer. GORM's pool settings must be set from env, not defaulted.

### 3.4 NATS — decide, don't ship "optional"

NATS JetStream is optional in code (degrades with a warning) and the durable work queue is already in Postgres. **For managed v1, do not run NATS.** Event streaming that currently rides NATS should either move onto the Postgres outbox or be explicitly disabled. Running a stateful NATS cluster on GKE for a feature that "degrades gracefully" is cost without a v1 customer asking for it.

## 4. Secrets & key custody (Cloud KMS)

Managed inverts the secret blast radius: Kiwi now holds the encryption key, every org's sealed provider/VCS credentials, and each daemon's private key. Two changes are non-negotiable before a paying customer:

### 4.1 The encryption key moves to Cloud KMS

`KIWI_ENCRYPTION_KEY` is a single symmetric key in an env var, and `crypto.EncryptAtRest` uses it directly. There is **no rotation path** — losing or leaking it exposes every org's credentials, and rotating it is impossible without a re-encryption tool.

Move to **envelope encryption**: a Cloud KMS key encrypts per-write data-encryption keys (or, given credential blobs are tiny, KMS `Encrypt`/`Decrypt` directly). This gives:
- IAM-gated access to decryption, audited in Cloud Audit Logs.
- **Key rotation** via KMS key versions, with old versions retained to decrypt existing rows.
- No long-lived symmetric key sitting in an environment variable.

Provide a one-time migration that re-wraps existing `EncryptAtRest` rows under KMS.

### 4.2 Daemon key custody is Kiwi-internal

In BYOC the daemon's X25519/Ed25519 keys live on the customer's disk. In managed they live on a GCE VM in our project. Join tokens are minted **internally** (the provisioner calls `POST /api/v1/daemon/join-token`, §5.2) rather than handed to a customer. Persist daemon keys to the VM's boot disk (encrypted with a CMEK) so a VM restart keeps its identity — re-registration needs a fresh single-use token.

### 4.3 Bootstrap secrets

`KIWI_SERVER_TOKEN` (admin bootstrap, resolves to org `system`) and provider keys used for internal/dev runs live in **Secret Manager**, surfaced to Cloud Run as env at deploy. Never in the image, never in `git`.

## 5. Execution zone — per-org daemon VMs

### 5.1 One GCE VM per org (M1)

A small always-on VM (`e2-small` class) per active org, in the segmented daemon subnet. Startup script installs the pinned `kiwidaemon`, mounts a persistent cache disk (`-cache-dir`), and runs it against the public API with an internally-minted join token. This is deliberately boring and hand-provisioned — at ten customers it is ~$50–200/mo, far cheaper than an autoscaler in engineer-days (Managed RFC §3.2.1).

### 5.2 Provisioning flow

A control-plane operation (`kiwid` admin endpoint or a small `opsctl` command), invoked by hand or a thin script when an org upgrades to managed:
1. mint an internal join token for the org (`POST /api/v1/daemon/join-token`, org-scoped — already exists);
2. `terraform apply` the per-org VM module with that token injected via instance metadata (not baked into an image);
3. the VM boots, `kiwidaemon` registers, heartbeats, and starts leasing that org's queue.

No autoscaler, no pod-per-org controller. A `terraform destroy` (or a `hibernate` flag, M3) reclaims it.

### 5.3 VM hardening (the price of Docker-in-v1)

Because v1 may ship the Docker sandbox as an interim (§6), each daemon VM is hardened as if already compromised (Managed RFC §4.2):
- **No ambient credentials.** Attach a service account with **no roles** (or none at all); the daemon needs no GCP API access. Set `enableOSLogin` off, block project SSH keys, disable the metadata `default` service-account token where possible.
- **Network segmentation.** Egress firewall = default-deny. Allow only: the public API load balancer, the model API endpoints, and VCS (GitHub) — via Cloud NAT with an FQDN/CIDR allowlist. **No route to the control zone's private surface** (Cloud SQL, KMS, other daemon VMs).
- **Shielded VM** (secure boot, vTPM, integrity monitoring) on; serial console off.
- Ingress: none. Daemons are outbound-only pollers.

## 6. Isolation substrate (M2) — GCE nested-virt + Firecracker

The sandbox is `pkg/sandbox` = Docker v1 (`docker run --network none`, mem/cpu caps). The managed target is a **microVM per sandbox** (Managed RFC §4.2): a dedicated guest kernel behind a KVM boundary, so a container/kernel escape does not reach the VM host — and therefore does not reach our project.

**GCP realization:** the per-org GCE VM is created with **nested virtualization enabled** (`--enable-nested-virtualization`, available on N2/C2/C3 with Intel VT-x), which exposes `/dev/kvm`. A new **Firecracker driver behind the existing `sandbox` interface** launches one microVM per task, mounts the worktree, runs the test command, and tears it down. The `sandbox` interface stays; only the driver is new, selected by env (`KIWI_SANDBOX=firecracker|docker`).

We do **not** build an isolation runtime (Managed RFC §4.2) — Firecracker is ~50k lines maintained by AWS. We wrap it.

Docker remains the driver for local/dev and BYOC; Firecracker is the managed default once M2 lands. Shipping v1 on Docker is allowed **only** with §5.3 hardening and as a stated, time-boxed decision.

## 7. Cross-cutting correctness (blockers surfaced by managed)

### 7.1 Idempotent PR creation (Managed RFC Open Q #2)

The lease queue is at-least-once: a daemon crash after opening a PR but before reporting redelivers the task, and a naive retry opens a **duplicate PR** — which, in managed, we are blamed for. Make delivery idempotent: key the PR on `(job_id, worker_id)` (branch name already is `kiwi/<jobID>`), and before creating, check for an existing open PR / branch and adopt it. Required before managed launch.

### 7.2 Orchestrator idempotency under concurrency

§3.1 pins the orchestrator to a singleton because `RequeueExpiredLeases` and friends are not proven safe to run concurrently. Either prove/keep them idempotent (transactional, `SKIP LOCKED`) and lift the singleton constraint, or keep `min=max=1` and accept it as the availability floor. Decide before scaling the control zone.

### 7.3 Protocol versioning (Managed RFC Open Q #3)

We upgrade managed daemons ourselves; BYOC customers upgrade on their own cadence. The heartbeat and `worker-spec.json` need an explicit `schema_version` and a min-supported check, or the first breaking change strands BYOC daemons when the ladder (M4) ships.

### 7.4 Free-tier abuse (Managed RFC Open Q #4)

A zero-friction tier running model-generated code on our hardware invites cryptomining and egress abuse. The §5.3 egress allowlist plus `org_limits` (per-job budget, concurrency, task timeout — already modeled) must be **enforced**, not just stored, and bounded before opening a free tier.

## 8. Observability & CI/CD

- **Logging** — `kiwid` logs to stdout; Cloud Run/GCE ship it to Cloud Logging automatically. Move to structured (JSON) logs so fields are queryable.
- **Tracing/metrics** — add an OpenTelemetry exporter → Cloud Trace + Cloud Monitoring; expose golden signals (lease latency, loop duration, provider cost, task success rate) and alert on them before onboarding a design partner.
- **CI/CD** — CI (tests) exists in GitHub Actions. Add CD: build image → **Artifact Registry** (Cloud Build on merge to `main`), run the migration Job, then roll Cloud Run revisions. Pin the daemon image tag the provisioner deploys, so managed daemon upgrades are deliberate.

## 9. Phasing (maps to the Managed RFC)

| This RFC | Managed RFC | Ships |
|---|---|---|
| §3, §4.1, §4.3, §3.2, §8 CI/CD | (foundation for M1) | Control Plane on Cloud Run + Cloud SQL + KMS + migrations Job |
| §5, §4.2 | **M1** | Per-org hand-provisioned daemon VMs, hardened |
| §6, §7.1 | **M2** | Firecracker sandbox driver; egress proxy; idempotent PR |
| §7.4, §8 obs | **M3** | Enforced quotas/metering; observability |
| — | M4 | BYOC ladder — customer runs the same VM module in their project |

The executable breakdown is in **`TODO_MANAGED_GCP.md`**.

## 10. Open questions

1. **Cloud Run vs GKE for the control zone.** Cloud Run is chosen for the API/frontend (no cluster to run). If per-task GKE Jobs later beat per-org GCE VMs for the execution zone, revisit — but only when §3.2.2's forcing functions arrive, not before.
2. **Firecracker on GCE nested-virt performance.** Nested virt adds overhead; validate cold-start (~125ms Firecracker + worktree mount) is acceptable on the chosen machine family before committing M2 to it vs. gVisor as a weaker fallback.
3. **Region & data residency.** Single region for v1. Multi-region/residency is a BYOC-graduation argument (M4), not a managed-v1 one.
4. **KMS `Encrypt` per credential vs envelope DEKs.** Credential blobs are small; direct KMS calls may be simplest, but audit-log volume and latency per task should be measured before choosing.
