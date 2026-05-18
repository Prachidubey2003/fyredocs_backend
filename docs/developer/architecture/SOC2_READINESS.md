# SOC 2 readiness assessment — Fyredocs

**Author:** engineering
**Last reviewed:** initial draft
**Audience:** founders, engineering leads, future auditor onboarding
**Scope:** the Fyredocs platform as built today (backend microservices + React/Vite frontend; mobile and AI tiers explicitly out of scope until they ship).

This document is an honest **self-assessment** — it answers two questions:

1. Of the SOC 2 Trust Services Criteria (TSC), which controls **already have technical evidence** from the work landed in Phase 0?
2. Of the remaining controls, which can engineering close vs. which need an organizational owner (HR, legal, compliance) — and roughly in what order?

It is **not** a SOC 2 report and does not substitute for a CPA's audit. It is the document we hand to the auditor when we kick off Phase 5's Type II observation window, plus the working punch list until then.

---

## 1. Scope decisions

### 1.1 Trust services categories we intend to be in scope

| Category | Code | In scope for the first SOC 2 report? | Why |
|---|---|---|---|
| Security | CC1–CC9 | **Yes** | Required for every SOC 2 report; it's the floor. |
| Availability | A1 | **Yes** | We sell a "your documents are processed reliably" product; SLAs are part of the value prop. |
| Confidentiality | C1 | **Yes** | Customer documents are confidential by default; signed/legal docs especially. |
| Processing Integrity | PI1 | **Yes (target Phase 5)** | Fyredocs transforms documents — integrity of output vs. input is the whole point. Phase 0's SHA-256 layer is the foundation; full PI controls land alongside the editor in Phase 1. |
| Privacy | P1–P8 | **No (deferred)** | Privacy adds the largest organizational surface (notice, choice, consent, retention). Defer until Phase 5 enterprise tier unless an enterprise prospect demands it sooner. |

### 1.2 System boundary

In scope:
- All services under [`fyredocs_backend/`](../../../README.md) (production code paths only — `*_test.go` and developer tooling are documentary, not productive).
- The frontend at the production URL.
- Postgres + Redis + NATS production instances.
- The `/files/` storage layer and its backup/snapshot infrastructure ([STORAGE.md](STORAGE.md)).
- The CI/CD pipeline insofar as it gates production deploys.

Out of scope (until they exist or are placed in scope):
- The mobile app (`fyredocs_mobile`) — not yet built.
- AI workloads — not yet built.
- Development workstations — covered by separate endpoint-management controls.

### 1.3 Report type sequencing

We pursue SOC 2 **Type I** first (point-in-time design opinion). Once Type I is clean and the population of evidence stabilises, we open the **Type II** observation window — minimum 3 months, target 12 — to demonstrate operating effectiveness over time.

---

## 2. Executive summary

Phase 0 has landed a meaningful slice of the **technical** Security controls (CC6 / CC7 / CC8) and most of the **CI-evidence** Availability controls. What's still missing is overwhelmingly **organizational** (CC1–CC2, parts of CC3 / CC4 / CC9), and a handful of operational controls that require either a deployed environment we haven't fully stood up (CC7 monitoring + alerting) or vendor work (BCP, vendor risk reviews).

In rough percentages, against the controls we'd expect a Type I report to test:

| Category | Tech evidence in place | Organizational evidence in place | Gap |
|---|---|---|---|
| CC1 — Control Environment | n/a | low | Most work organizational |
| CC2 — Communication and Information | partial | low | Code-of-conduct, security training, customer-facing security page |
| CC3 — Risk Assessment | low | low | Annual formal risk assessment; threat model |
| CC4 — Monitoring | medium | low | Logs exist; runbook/oncall + incident-management process missing |
| CC5 — Control Activities | medium | low | CI gates exist; segregation-of-duties policy + reviewer matrix missing |
| CC6 — Logical & Physical Access | **high** | medium | Auth, JWT rotation runbook, secret store; off-boarding + access reviews missing |
| CC7 — System Operations | **high** | medium | Backups, monitoring, scans; written incident-response policy + tabletop missing |
| CC8 — Change Management | **high** | low | CI gates, branch protection, OpenAPI spec contract; change-approval policy doc missing |
| CC9 — Risk Mitigation | low | low | Vendor risk management, insurance evidence |
| A1 — Availability | medium | low | SLOs, backup strategy land; published SLA + DR drill record missing |
| C1 — Confidentiality | medium | low | Encryption + per-tenant isolation plan; data classification policy missing |
| PI1 — Processing Integrity | low | low | SHA-256 plumbed; editor processing integrity (Phase 1) outstanding |

---

## 3. Criterion-by-criterion mapping

### CC1 — Control environment

| Criterion | Status | Evidence (today) | Gap (must close before Type II window opens) |
|---|---|---|---|
| CC1.1 Demonstrates commitment to integrity & ethical values | ❌ | none | Code of conduct + ethics policy signed by every staff member. |
| CC1.2 Board exercises oversight | ❌ | none | If we have a board / advisory: minutes referencing security topics ≥ quarterly. |
| CC1.3 Establishes structure, authority, responsibility | ❌ | [CLAUDE.md](../../../CLAUDE.md) and `docs/developer/` describe engineering structure | Org chart, RACI for security decisions. |
| CC1.4 Demonstrates commitment to competence | ❌ | none | Job descriptions + security training records. |
| CC1.5 Enforces accountability | ❌ | none | Performance reviews referencing security responsibilities; disciplinary policy. |

**Owner:** founders / HR. None of this is engineering work. Plan: pull a SOC 2 policy template (Vanta / Drata / Secureframe ship them) and adapt; do not write from scratch.

### CC2 — Communication and information

| Criterion | Status | Evidence (today) | Gap |
|---|---|---|---|
| CC2.1 Internal information re: security | 🟡 | [CLAUDE.md](../../../CLAUDE.md), [docs/developer/architecture/](.) — all our security docs (this one, [SECRETS.md](SECRETS.md), [STORAGE.md](STORAGE.md), [CI_CD.md](CI_CD.md), [OPENAPI.md](OPENAPI.md)) are in-repo and versioned | Quarterly security email; written acknowledgement that staff have read core policies. |
| CC2.2 Communicates with external parties (customers, regulators) | 🟡 | [SECURITY.md](../../../SECURITY.md) discloses how to report a vulnerability and our response SLA | A customer-facing `/trust` page; status page (`status.fyredocs.com`). |
| CC2.3 Communicates externally — security | 🟡 | [SECURITY.md](../../../SECURITY.md), public OpenAPI spec | DPA template, sub-processor list. |

**Owner:** engineering + marketing for the trust page; legal for DPA / sub-processor disclosures.

### CC3 — Risk assessment

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC3.1 Specifies suitable objectives | 🟡 | [Plan blueprint](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md) sets measurable goals (SLOs, performance budgets) | Formal "security & availability objectives" register tied to the SOC 2 categories above. |
| CC3.2 Identifies risk | 🟡 | [STORAGE.md threat model](STORAGE.md#threat-model) (storage-layer); plan §1 captures user-impact risks | Annual enterprise-wide risk register; threat-model documents for every other service. |
| CC3.3 Considers fraud risk | ❌ | none | Add "fraud risk" sub-section to risk register (payment fraud, account takeover, refund abuse). |
| CC3.4 Identifies & assesses changes | 🟡 | Quarterly review cadence implied; no formal trigger | "Change-of-scope" review policy: when a new service / sub-processor / data class lands, security review is mandatory. |

**Owner:** engineering writes the risk register; founders sign off annually.

### CC4 — Monitoring

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC4.1 Conducts ongoing or separate evaluations | 🟡 | CI gates (gosec / govulncheck / CodeQL / OSV / Trivy / staticcheck / gitleaks) — see [`security.yml`](../../../.github/workflows/security.yml) | Quarterly internal security review with documented findings; annual pen test. |
| CC4.2 Communicates control deficiencies | 🟡 | All scanners post SARIF to the GitHub Security tab → assigned to engineering | Written escalation matrix when a Critical finding lands. |

**Owner:** engineering. Mostly procedural — define "review meeting cadence + minutes" and we're done. Pen test: engage a vendor before Type II window opens.

### CC5 — Control activities

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC5.1 Selects and develops control activities | 🟢 | Pre-merge gates: gofmt, staticcheck, gosec, govulncheck, gitleaks, CodeQL, OSV-Scanner, OpenAPI lint, Trivy on every image, race-test, SBOM | None for code; need an equivalent for non-engineering changes (e.g., production database schema changes). |
| CC5.2 Selects and develops general controls over technology | 🟢 | Per-service Dockerfiles use scratch base + non-root user + healthchecks; mTLS plan in [CI_CD.md](CI_CD.md) Phase 5 | Documented network segmentation diagram. |
| CC5.3 Deploys through policies and procedures | 🟡 | [CLAUDE.md](../../../CLAUDE.md) enforces test+docs updates on every change | Branch protection on `main` (GitHub setting; outside repo); written code-review policy (≥ 1 reviewer, security-relevant changes need security-tagged reviewer). |

**Owner:** engineering manager. Two GitHub UI changes plus one written policy.

### CC6 — Logical and physical access controls

This is our **strongest** category — most of Phase 0 landed here.

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC6.1 Implements logical access security software | 🟢 | [auth-service](../../../auth-service/), JWT HS256 with 8h access + 168h refresh, HttpOnly cookies, refresh-token rotation | — |
| CC6.2 Registers, authorizes, modifies user accounts | 🟡 | Signup → email-verified user; role-gating for admin routes ([App.tsx routing](../../../../../fyredocs_frontend/src/App.tsx)) | No formal access-review cadence; no offboarding automation (when a staff member leaves, accounts must be revoked within 24h per SOC 2 expectation). |
| CC6.3 Restricts logical access | 🟢 | RoleRoute guards super-admin routes; rate limits on login/signup/refresh | Need session-revocation API surfaced to admin UI. |
| CC6.4 Restricts physical access | 🟡 | Production runs on cloud + bare-metal hosts; physical security inherited from datacenter | Capture the SOC 2 / ISO 27001 attestations of the underlying datacenter as evidence (e.g., Hetzner, AWS, Cloudflare). |
| CC6.5 Discontinues access | 🟡 | JWT denylist exists ([auth-service](../../../auth-service/)) | Offboarding runbook: revoke GitHub access, GHCR access, hosting access, secret-store access — all within 24h. |
| CC6.6 Implements logical access security measures against threats from outside the system | 🟢 | TLS 1.3 at the gateway, gosec/CodeQL gates, gitleaks, [SECURITY.md](../../../SECURITY.md) disclosure policy | WAF rules tuned (Cloudflare default OK as baseline). |
| CC6.7 Restricts data transmission & disposal | 🟢 | TLS in transit; restic encrypted backups ([STORAGE.md §backups](STORAGE.md#backups-and-snapshots)); per-tenant dataset destroy on offboarding (planned) | Customer-facing "delete my data" SLA — current TTL is 24h for free / 30 days for paid; document in [SECURITY.md](../../../SECURITY.md). |
| CC6.8 Prevents/detects unauthorized software | 🟡 | Trivy on every image, OSV-Scanner, Dependabot grouped updates | Endpoint protection on developer laptops (MDM); workstation policy. |

**Owner:** engineering for the GitHub / secret-store offboarding playbook; founders for the MDM rollout.

### CC7 — System operations

Second-strongest category for us — Phase 0 landed most of the technical pieces.

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC7.1 Monitors system components for anomalies | 🟢 | OpenTelemetry tracing per service ([CLAUDE.md §6](../../../CLAUDE.md)); Prometheus `/metrics`; `analytics-service` ingests events | Production dashboards in Grafana; written alert routing matrix. |
| CC7.2 Monitors security incidents | 🟡 | GitHub Security tab is populated by every scanner; gitleaks on full history | Pager rotation defined; SLA on Critical findings (e.g., 24h to mitigate). |
| CC7.3 Evaluates and responds to security events | 🟡 | [SECURITY.md](../../../SECURITY.md) policy committed; [SECRETS.md §7](SECRETS.md#7--incident-response-a-secret-has-leaked) defines incident-response procedure for secret leaks | Generalize to non-secret incidents; tabletop exercise quarterly. |
| CC7.4 Identifies/develops/implements activities to recover from incidents | 🟡 | Backup strategy ([STORAGE.md §evolution-path](STORAGE.md#evolution-path)); restic + ZFS snapshots; restore runbook in [deployment/storage/README.md](../../../deployment/storage/README.md) | Documented RTO/RPO; quarterly DR drill record. |
| CC7.5 Identifies / develops / implements monitoring | 🟢 | OTel + Prometheus + structured logs in every service | Grafana dashboards as Git-managed JSON; alert rules. |

**Owner:** engineering. Most missing items are "write the dashboard JSON" + "schedule the drill on the calendar".

### CC8 — Change management

**Strongest** category — this is what Phase 0 was largely about.

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC8.1 Authorizes changes | 🟢 | PR + required status checks (build/test/lint/security/openapi); per-service mandatory test updates ([CLAUDE.md §9](../../../CLAUDE.md)) | Branch protection rule set on `main` (GitHub setting). |
| CC8.2 Documents changes | 🟢 | Commits, PR descriptions, [OpenAPI spec](../swagger/openapi.yaml) drift-detected, [CLAUDE.md §8](../../../CLAUDE.md) mandatory docs updates | Release notes per deploy (already partly via commit log; need a `CHANGELOG.md`). |
| CC8.3 Tests changes | 🟢 | Go race tests per service, OpenAPI lint, Playwright smoke, Trivy on image, gosec/CodeQL | Coverage threshold gate (target ≥ 70% per service per Phase 0 plan); Phase 1 will add golden-PDF SSIM gate. |
| CC8.4 Implements changes | 🟢 | Per-service Docker images with semver + SHA tags; GHCR is the source of truth | Documented rollback procedure per service (≥ keep `latest-1` tag available). |
| CC8.5 Conducts post-implementation review | 🟡 | Sentry captures runtime errors (Phase 0 — not yet wired) | Written postmortem template + cadence (every Sev-1/Sev-2 → blameless PM within 7d). |

**Owner:** engineering. Mostly documentation of practices already in place.

### CC9 — Risk mitigation

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| CC9.1 Implements processes to identify risks | 🟡 | CC3 register (to be written); CI scanners | Annual review with auditor; insurance review. |
| CC9.2 Manages vendor risk | ❌ | none | Vendor inventory + risk tier each: Cloudflare, GitHub, Postgres host (Neon/self-host), Backblaze, etc. SOC 2 reports for each. |

**Owner:** founders / legal. Vanta / Drata automate the vendor inventory + auto-collect their SOC 2 reports — buy the tool.

### A1 — Availability

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| A1.1 Maintains, monitors & evaluates current processing capacity | 🟡 | OTel metrics; per-service Prometheus | Capacity-planning record; quarterly review. |
| A1.2 Recovers from incidents | 🟡 | Backup + snapshot strategy ([STORAGE.md](STORAGE.md)); restore runbook | Published RTO ≤ 30 min / RPO ≤ 5 min targets from plan §9.4 — confirm by DR drill. |
| A1.3 Tests recovery procedures | ❌ | none | Quarterly drill with timestamped log of restore time. |

**Owner:** engineering / SRE.

### C1 — Confidentiality

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| C1.1 Identifies and maintains confidential information | 🟡 | [STORAGE.md threat model](STORAGE.md#threat-model); customer files are per-owner partitioned | Written data-classification policy: public / internal / confidential / restricted. |
| C1.2 Disposes of confidential information | 🟢 | [cleanup-worker](../../../cleanup-worker/) sweeps expired files; ZFS snapshots age out; restic forget | Customer "delete on request" SLA + audit log. |

### PI1 — Processing integrity (target: Phase 1)

| Criterion | Status | Evidence | Gap |
|---|---|---|---|
| PI1.1 Defines processing-integrity objectives | 🟡 | Plan §9.2 specifies measurable acceptance criteria (SSIM ≥ 0.98, qpdf check, veraPDF) | Customer-facing statement: "Edits never silently corrupt non-edited regions; we measure it." |
| PI1.2 Authorizes inputs | 🟡 | Auth at gateway; per-job ownership in `file_metadata` | DLP scanning on inputs (Phase 5). |
| PI1.3 Processes data accurately | 🟡 | Worker services emit progress + completion events; SHA-256 column added to `file_metadata` ([STORAGE.md §integrity](STORAGE.md#integrity--sha-256-checksums)) | Wire `TeeHasher` into worker write paths so the column is actually populated for new rows (open follow-up todo). |
| PI1.4 Processes data completely | 🟢 | NATS JetStream with retries + DLQ ([CLAUDE.md §4](../../../CLAUDE.md)) | Reconciliation job: jobs stuck > 1h in `processing` → alert. |
| PI1.5 Produces verifiable output | 🟡 | SHA-256 plumbed; periodic verification ([deployment/storage/verify-checksums.sh](../../../deployment/storage/verify-checksums.sh)) | Phase 1 SSIM gate against golden corpus. |

---

## 4. Punch list (priority order)

What we should land before opening the Type II window. **Bold = engineering owns**; *italic = founders/HR/legal/ops own*.

**Phase 0 follow-ups (already on the todo list):**
- **Wire `TeeHasher` into worker write paths** so `sha256_hash` is populated for new rows (PI1.3).
- **Implement dual-key JWT verification** so the rotation runbook ([SECRETS.md §3](SECRETS.md#3--jwt-hs256-signing-key-jwt_hs256_secret)) is zero-downtime in practice, not just on paper (CC6.1).

**Engineering-led (≈ 1–2 sprints):**
- **Add branch protection on `main`**: required reviewers, required status checks, no force pushes. GitHub UI change; document in [CI_CD.md](CI_CD.md). (CC5.3, CC8.1)
- **Wire Sentry** in every service so CC8.5 has post-implementation evidence. (CC8.5)
- **Git-managed Grafana dashboards** and Prometheus alert rules; route to a single pager channel. (CC7.1, CC7.5)
- **Quarterly DR drill** scripted in [`deployment/storage/dr-drill.sh`](../../../deployment/storage/) — runs a restore into staging and writes a timestamped log. (A1.3, CC7.4)
- **`CHANGELOG.md`** generated from PR titles per release. (CC8.2)
- **Per-service `runbook.md`** in each service's directory, linked from [`/docs/developer/services/<service>.md`](../services/). Covers: health, common alerts, paging contacts. (CC7.3)
- **Reconciliation cron** for stuck jobs (status=processing AND updated_at < now - 1h) → alert. (PI1.4)
- **Coverage gate** in CI: fail if any service drops below the per-service baseline by > 5 percentage points. (CC8.3)

**Organizational (mostly outside engineering — buy a SOC 2 platform):**
- *Code of conduct + acceptable-use + access-control + change-management + incident-response policies* — adapt Vanta/Drata templates, do not write from scratch.
- *HR onboarding + offboarding checklist* (revoke GitHub, GHCR, secret store, Postgres, hosting within 24h). (CC6.2, CC6.5)
- *Annual security training* (one-hour video + quiz; SOC 2 platforms ship this).
- *Vendor inventory + SOC 2 collection*: Cloudflare, GitHub, hosting provider, Postgres provider, Redis provider, NATS provider, Backblaze (or other backup target), Sentry, OpenTelemetry vendor (if any). (CC9.2)
- *MDM on developer laptops* with FileVault/BitLocker enforced. (CC6.8)
- *Insurance evidence* (cyber liability) — usually a procurement task. (CC9.1)
- *Annual penetration test* engagement, ≥ 8 weeks before the Type II observation window opens. (CC4.1)

**Stack/platform decisions to make now (so engineering doesn't waste effort):**
- Pick a SOC 2 evidence platform: **Vanta** (most common; expensive), **Drata** (slightly cheaper, similar product), **Secureframe**, or **Oneleet** (engineer-friendly, lower price). Recommendation: Vanta or Drata for the first audit unless cash flow is tight; the agent integrations save weeks. Document the decision in `docs/developer/architecture/COMPLIANCE_PLATFORM.md` once made.
- Pick a CPA firm. Common in tech: **Prescient Assurance**, **Insight Assurance**, **AssuranceLab**, **A-LIGN**. Lower price points exist (Schellman is overkill for a startup). Engage at the same time as the platform.
- Pick an MDM (for CC6.8 + CC1.4 evidence): **Kandji** (Mac-centric), **JumpCloud** (mixed), **Jamf Now** (Mac, cheap).

---

## 5. Evidence catalogue (what auditors will ask for)

When the auditor reaches out, hand them this list of artifacts. Everything that already exists is link-included; everything that does not is the punch list above.

| Evidence | Where |
|---|---|
| Architecture overview | [`README.md`](../../../README.md), [`docs/developer/`](..) |
| Microservice rules | [`CLAUDE.md`](../../../CLAUDE.md) |
| Vulnerability disclosure policy | [`SECURITY.md`](../../../SECURITY.md) |
| Secret inventory + rotation runbook | [`SECRETS.md`](SECRETS.md) |
| Storage integrity + backups | [`STORAGE.md`](STORAGE.md) |
| CI gates + workflows | [`CI_CD.md`](CI_CD.md), [`.github/workflows/`](../../../.github/workflows/) |
| OpenAPI contract + drift gate | [`OPENAPI.md`](OPENAPI.md), [`docs/developer/swagger/openapi.yaml`](../swagger/openapi.yaml) |
| Security scans (SARIF) | GitHub Security tab on the repo |
| Dependency-update history | Dependabot PR history |
| SBOM | `sbom-backend.spdx.json` and `sbom-frontend.spdx.json` artifacts (14-day retention; persist quarterly to S3 for audit) |
| Code-review evidence | PR history with required reviewers + checks |
| Backup integrity | `restic check --read-data-subset=5%` log from each nightly run |
| Snapshot evidence | ZFS `zfs list -t snapshot` output per host, archived weekly |
| Deployment history | GHCR image digests + commit SHA correlation; CI run logs (1-year retention) |
| Incident records | Future: `runbooks/incidents/` directory; today: GitHub Security advisories |
| DR drill records | Future: quarterly drill log |
| Risk register | Future: `docs/developer/architecture/RISK_REGISTER.md` |
| Vendor list with SOC 2 reports | Future: managed by SOC 2 platform |
| Org chart + HR records | Future: managed by SOC 2 platform / HRIS |

---

## 6. Recommended sequencing

**T+0 (now):** Land Phase 0 follow-ups + engineering-led punch-list items above. Buy a SOC 2 platform; have it auto-collect what it can.

**T+30 days:** All engineering-led items in scope. Founders complete organizational policies via SOC 2 platform templates. Engage CPA firm; receive readiness assessment.

**T+60 days:** Address auditor's gap letter. Quarterly DR drill #1. First quarterly security review. Vendor inventory closed.

**T+90 days:** Type I readiness — auditor performs Type I procedures. Issue Type I report.

**T+90 → T+180 days:** Type II observation window opens. Operate every control on its cadence; collect evidence. Do **not** miss a single backup verification, dependency-update review, or DR drill during the window.

**T+180 → T+365 days:** Continue observation. Land Phase 5 enterprise items in parallel (SSO, audit chain, DLP — see [main plan](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)). Optional: extend window to 12 months for the strongest report shape.

**Year-end:** Type II report issued. Annual renewal cycle thereafter.

---

## 7. What this document is not

- **Not** a guarantee of audit success. The auditor decides.
- **Not** legal advice. Particularly around CC1, CC2, P-series, HIPAA, GDPR — get qualified counsel. (For HIPAA specifically, the engineering-authored BAA template at [HIPAA_BAA_TEMPLATE.md](HIPAA_BAA_TEMPLATE.md) is the starting point we hand to legal — it maps every safeguard commitment to a live control in this repo.)
- **Not** a substitute for actually doing the work. The status columns above mark "evidence exists in our system today", not "the auditor will accept this." Many controls that are 🟢 here will need additional sample evidence (e.g., 12 months of dependency-update PRs) during Type II testing.
- **Not** static. Update this document whenever a control changes; date the "last reviewed" header in the front matter at every review.
