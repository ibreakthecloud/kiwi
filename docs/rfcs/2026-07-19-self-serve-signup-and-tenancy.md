# RFC: Self-Serve Signup & Tenancy

**Date:** 2026-07-19
**Status:** Proposed
**Depends on:** [Managed Execution Tier RFC](2026-07-17-managed-execution-tier-rfc.md) · [Managed Tier on GCP](2026-07-19-managed-tier-gcp-deployment.md)
**Related:** [Architecture Review](../design/2026-07-16-byoc-architecture-review.md)

## 1. Summary

Kiwi has **no self-serve signup today.** Orgs, users, and API keys are created by an
admin through `/admin/*` endpoints gated by `KIWI_SERVER_TOKEN` (this is what
`deploy/bootstrap.sh` does); the dashboard's login only *simulates* OAuth. That is
correct for the hand-provisioned design-partner phase the Managed RFC assumes — and
it does not scale to strangers on the internet.

This RFC defines how people sign up, what an "org" is, when we spend money on a VM,
and how a personal `@gmail.com` user differs from a company `@acme.com` user. It
rests on **one load-bearing decision**:

> **Creating an org is not the same event as provisioning execution capacity.**

- **Org creation** — instant, free, cheap: a row + default limits. Happens at signup.
- **Capacity provisioning** — the per-org always-on GCE daemon VM from the Managed
  GCP RFC. Expensive. Happens on **activation**, not signup.

Conflating the two is what makes self-serve appear to contradict the Managed RFC's
"one always-on VM per org, hand-provisioned, no autoscaler." It does not: **signup
creates the org; the VM is deferred until the org activates** (pays, or is manually
approved). A thousand tire-kickers cost a thousand rows, not a thousand VMs.

## 2. Non-goals / inherited decisions

- **No free *execution* in v1.** A free tier that runs model-generated code on our
  hardware needs a shared execution pool, and a shared pool breaks the per-org
  credential-sealing model (credentials are sealed to *one daemon's* key — Managed
  RFC §3.2) and needs the unbuilt Firecracker + egress isolation (Managed RFC M2).
  **v1 is pay-to-run:** free accounts can sign up, connect repos, and *plan/preview*
  tasks; **running** a task requires activation. A free shared-pool tier is a
  post-M2 decision, not this RFC's.
- **No new isolation model.** Tenancy still = `org_id` on every row + one daemon per
  activated org. This RFC adds *who creates orgs and how*, not *how they're isolated*.
- **API keys stay.** OAuth is for humans in the browser; API keys remain for CLI/CI
  (`kiwi submit`). OAuth mints the first API key; users can manage more.

## 3. Signup & authentication

### 3.1 GitHub OAuth is the primary path

Sign-in is OAuth, and **GitHub is the primary provider**, because it is doubly
useful: the product must push branches and open PRs, so the GitHub grant *is* the
VCS credential we already need (`GIT_TOKEN` today). One consent covers identity and
repo access. **Google OAuth** is the secondary path for users who evaluate before
connecting a repo.

Flow:
1. User clicks "Continue with GitHub/Google".
2. We receive a **provider-verified email**, a name, and (GitHub) a repo grant.
3. Resolve or create the `User`; associate to an `Org` per §4; issue a browser
   session; mint an initial API key for CLI use.
4. Land in the dashboard (or onboarding if the org is brand-new).

No passwords. The admin-token path (`/admin/*`) is retained for internal/support use
only, not exposed to signup.

### 3.2 What signup writes

- a `User` (email unique, provider identity),
- an `Org` (personal or team, §4) with **default free-tier `OrgLimits` and
  `activation_state = inactive`** (no daemon),
- a browser session + an initial API key.

Nothing provisions a VM. `/readyz`-style "can this org run?" is a function of
`activation_state`, checked at submit time (§5).

## 4. What an org is, and `@gmail` vs `@acme.com`

An **org is the tenant boundary** — the unit that carries `org_id`, holds `OrgLimits`,
is billed, and (once active) owns exactly one daemon. The consumer-vs-company split is
resolved from the **OAuth-verified email domain**:

### 4.1 Personal domains → personal org

If the verified email's domain is on a **maintained public-provider list**
(`gmail.com`, `outlook.com`, `icloud.com`, …), the user gets **their own individual
org** — a personal workspace. We **never** auto-join personal-domain users to each
other. Personal orgs default to the individual plan.

### 4.2 Company domains → shared org, verified join

If the domain is **not** on the personal list (`acme.com`), it is treated as a
company domain:

- The **first** verified user from `acme.com` creates the **company org** for that
  domain and becomes its admin.
- Subsequent verified `@acme.com` users are routed to that org:
  - if the org has **domain-join enabled** (an admin setting) → auto-join as member;
  - otherwise → **request to join**, an admin approves.
- We rely on the **IdP-verified** email for routing (Google/GitHub verify the
  address). We do **not** trust an unverified claimed domain.
- **Exclusive domain ownership** (claim `acme.com` so no rogue org can use it) is a
  later feature via DNS-TXT verification — not required for v1 routing.

This is the Slack/Linear/Notion model: verified-domain users converge on one
workspace instead of fragmenting into per-person orgs.

### 4.3 Org model changes

Add to `Organization` (today just `{id, name}`):
- `type` — `personal | team`
- `primary_domain` — the routing domain (empty for personal)
- `domain_join` — bool, admin-controlled auto-join for the domain
- `plan` — `free | individual | team | …`
- `activation_state` — `inactive | active | suspended` (drives provisioning, §5)

`User` keeps `{email, org_id, role}`; add the OAuth identity (`provider`, `subject`).
A user belongs to one org in v1; multi-org membership (personal **and** company) is a
follow-up (§7).

## 5. Activation → provisioning (where money is spent)

Execution is **gated on `activation_state`**, decoupled from signup:

```
signup ──► org (inactive, no daemon)
              │  connect repo, plan/preview tasks  ✅
              │  submit a task to RUN               ❌ 402 "activate to run"
              ▼
        activate (pay, or admin approve)
              │  set activation_state = active
              │  trigger provisioning (Managed GCP RFC):
              │    mint internal join token → opsctl/terraform → per-org GCE VM
              ▼
        org (active, one daemon) ──► tasks run, PRs open
```

- The **submit path** checks `activation_state`; an inactive org gets a clear
  `402`/`403` ("activate to run"), not a silent queue. Planning/preview is allowed
  while inactive so the product demonstrates value before payment.
- **Activation** is either a **payment event** (billing, §6) or a **manual admin
  approve** (for design partners / sales-led deals). Both set `active` and enqueue a
  **provisioning job** that runs the Managed GCP RFC flow (`cmd/opsctl` → per-org VM).
  v1 provisioning stays **hand-triggered / semi-automated — no autoscaler** (Managed
  RFC §3.2.1); the activation hook can create a provisioning *request* an operator
  fulfils, or call `opsctl` directly once trusted.
- **Deactivation / suspension** (non-payment, abuse) sets `inactive`/`suspended` and
  reclaims the VM (`terraform destroy` / hibernate).

This is the seam that reconciles self-serve with the per-org-VM model: **the funnel
is free and instant; the VM only exists for orgs that activated.**

## 6. Billing (the activation trigger)

Out of scope to fully specify here, but the shape it must have:
- A billing provider (e.g. Stripe) with a **checkout that flips `activation_state`**
  via webhook. Manual approve is the same state transition without a payment.
- Enforce `OrgLimits` (per-job budget, concurrency, timeout — modeled today, enforced
  by GCP-TODO phase G8) as the metered ceiling; usage/cost attribution per job.
- Plan → `OrgLimits` mapping (free/individual/team set different caps).

## 7. Open questions

1. **Multi-org membership.** A person may be both a personal org and a member of
   `acme.com`. v1 pins one org per user; a proper org-switcher + membership table is a
   fast follow. Decide before company orgs get real traction.
2. **Domain-join safety.** Auto-join by verified domain is convenient but means any
   `@acme.com` signup lands in acme's workspace. Default **domain-join off**
   (request+approve) until an org opts in; document the trade-off.
3. **Personal-provider list drift.** The public-domain list needs maintenance;
   mis-classifying a company domain as personal fragments their org. Use a maintained
   dataset and make it easy to correct.
4. **Session security.** OAuth introduces browser sessions (CSRF, cookie flags,
   fixation) that API-key auth avoided — treat as security-sensitive.
5. **Free-tier abuse** (shared with Managed RFC Open Q #4). Even "free = plan only"
   runs the planner (a model call) on our dime; rate-limit unactivated orgs.

## 8. Phasing

| Phase | Ships |
|---|---|
| S0 | Org/User model extensions (`type`, `primary_domain`, `domain_join`, `plan`, `activation_state`, OAuth identity) + migration |
| S1 | GitHub + Google OAuth sign-in, sessions, initial API-key mint |
| S2 | Domain routing: personal-provider list → personal org; company domain → shared org |
| S3 | Company-domain join (auto-join / request-approve) |
| S4 | Activation gate on the submit path (pay-to-run: block execution while inactive) + provisioning trigger |
| S5 | Billing checkout → activation webhook (+ manual-approve admin action) |
| S6 | Frontend: real login (replace the simulated one), onboarding, org/seat management |

Sequencing note: **hand-provisioned design partners (no signup) → self-serve
pay-to-run (this RFC) → free shared-pool tier (post-M2 isolation).**

The executable breakdown is in **`TODO_SIGNUP_TENANCY.md`**.
