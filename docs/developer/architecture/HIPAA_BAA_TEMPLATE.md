# HIPAA Business Associate Agreement — Fyredocs (template)

**Document type:** template — **NOT a signed legal instrument**.
**Status:** working draft for legal review. Bracketed `[…]` fields are placeholders. Sections marked **TODO-LEGAL** need outside-counsel sign-off before this template is shipped to a prospect.
**Last reviewed:** initial draft.
**Owner of record:** [Customer Success / Legal — TBD].

This file is the template Fyredocs offers to a [Covered Entity] when they request a HIPAA Business Associate Agreement ("BAA") so Fyredocs can lawfully process [Protected Health Information] on their behalf. It is engineering-authored to keep the **Safeguards** section honest — i.e., to make sure every commitment we sign maps to a real control in [fyredocs_backend/](../../../README.md). Legal customises wording and executes.

A few non-negotiables baked in here:
- Fyredocs does not contract around the [Breach Notification Rule] (§ 6) — the 60-day window is statutory.
- Fyredocs does not accept liability beyond what its insurance carrier underwrites — concrete cap goes in § 10.
- Fyredocs's only downstream sub-processors with PHI access are documented in **Schedule B** of this template; adding one is an amendment, not a unilateral notice.

---

## 1. Parties

This Business Associate Agreement (this "**Agreement**") is entered into by:

- **Covered Entity**: `[Customer legal entity name]`, a `[state]` `[entity type]`, with its principal place of business at `[address]` ("**Covered Entity**"); and
- **Business Associate**: `[Fyredocs legal entity name]`, a `[Delaware C-Corp / TBD]`, with its principal place of business at `[address]` ("**Business Associate**").

Each a "**Party**", together the "**Parties**".

**Effective Date**: `[YYYY-MM-DD]`. This Agreement supplements, and is incorporated into, the [Master Services Agreement / Order Form] between the Parties dated `[YYYY-MM-DD]` (the "**Underlying Agreement**"). Capitalised terms not otherwise defined have the meanings given in the Underlying Agreement; HIPAA terms have the meanings given in **§ 2 Definitions** below.

---

## 2. Definitions

Each capitalised term below has the meaning given in 45 CFR Parts 160, 162, and 164 (the "**HIPAA Rules**"), as amended by the Health Information Technology for Economic and Clinical Health Act, Pub. L. 111-5 (the "**HITECH Act**"), and any implementing regulations.

| Term | Meaning |
|---|---|
| **Breach** | As defined at 45 CFR § 164.402. |
| **Business Associate** | As defined at 45 CFR § 160.103. |
| **Covered Entity** | As defined at 45 CFR § 160.103. |
| **Designated Record Set** | As defined at 45 CFR § 164.501. |
| **Electronic Protected Health Information (ePHI)** | PHI transmitted by or maintained in electronic media. |
| **Individual** | As defined at 45 CFR § 160.103 (and includes a personal representative). |
| **Privacy Rule** | 45 CFR Part 160 and Part 164, Subparts A and E. |
| **Protected Health Information (PHI)** | As defined at 45 CFR § 160.103, limited to information Business Associate Creates, Receives, Maintains, or Transmits on behalf of Covered Entity under the Underlying Agreement. |
| **Required by Law** | As defined at 45 CFR § 164.103. |
| **Secretary** | The Secretary of the U.S. Department of Health and Human Services or any designee. |
| **Security Incident** | As defined at 45 CFR § 164.304. |
| **Security Rule** | 45 CFR Part 160 and Part 164, Subparts A and C. |
| **Subcontractor** | As defined at 45 CFR § 160.103. |
| **Unsuccessful Security Incident** | Pings, port scans, denial-of-service attempts, and similar attempts that did not result in unauthorised access to PHI. |

---

## 3. Permitted uses and disclosures of PHI

### 3.1 Performance of the Underlying Agreement
Business Associate may Use and Disclose PHI only to perform the functions, activities, or services for, or on behalf of, Covered Entity as set forth in the Underlying Agreement and in this Agreement, including the activities listed in **Schedule A**.

### 3.2 Specific permitted uses
Business Associate may Use PHI:
1. for the proper management and administration of Business Associate or to carry out the legal responsibilities of Business Associate, provided that any Disclosure for such purposes is either Required by Law or covered by § 3.3 below;
2. to provide Data Aggregation services to Covered Entity as permitted by 45 CFR § 164.504(e)(2)(i)(B), but **only** for the Covered Entity's own health-care operations and only on data sets that contain PHI of that Covered Entity (no cross-tenant aggregation);
3. to de-identify PHI in accordance with 45 CFR § 164.514(a)–(c). De-identified data is not PHI and may be used and disclosed by Business Associate for any lawful purpose.

### 3.3 Specific permitted disclosures
Business Associate may Disclose PHI:
1. as Required by Law;
2. for the proper management and administration of Business Associate, provided that the Disclosures are Required by Law, **or** Business Associate obtains reasonable assurances from the person to whom the PHI is disclosed that the PHI will be held confidentially and used or further disclosed only as Required by Law or for the purpose for which it was disclosed, and that the person notifies Business Associate of any instance of which it is aware in which the confidentiality of the PHI has been Breached.

### 3.4 Minimum Necessary
Business Associate's Uses, Disclosures, and Requests for PHI shall be limited to the minimum necessary to accomplish the intended purpose, consistent with 45 CFR § 164.502(b) and the Covered Entity's then-current Minimum Necessary policy.

---

## 4. Obligations of Business Associate

Business Associate agrees to:

1. **Not Use or Disclose PHI** other than as permitted or required by this Agreement, the Underlying Agreement, or as Required by Law.
2. **Use appropriate safeguards** — and, with respect to ePHI, comply with Subpart C of the Security Rule — to prevent Use or Disclosure of PHI other than as provided for by this Agreement. Specific technical, administrative, and physical safeguards are documented in **§ 5 Safeguards** below.
3. **Mitigate**, to the extent practicable, any harmful effect that is known to Business Associate of a Use or Disclosure of PHI by Business Associate in violation of this Agreement.
4. **Report** to Covered Entity any Use or Disclosure of PHI not provided for by this Agreement of which Business Associate becomes aware, including Breaches of Unsecured PHI as required at 45 CFR § 164.410, and any Security Incident of which it becomes aware (the reporting cadence + content is § 6 below).
5. **Ensure** that any Subcontractor that Creates, Receives, Maintains, or Transmits PHI on behalf of Business Associate agrees to the same restrictions, conditions, and requirements that apply to Business Associate under this Agreement. The current list of permitted Subcontractors is **Schedule B**.
6. **Make available** PHI in a Designated Record Set to Covered Entity as necessary to satisfy Covered Entity's obligations under 45 CFR § 164.524. Business Associate's response window: ten (10) business days from receipt of a written request from Covered Entity. Where the underlying data is stored in a customer-tenant directory under the per-tenant LUKS mount described in [STORAGE.md](STORAGE.md), the request triggers an export job via the existing [editor-service download path](../services/EDITOR_SERVICE.md).
7. **Make amendments** to PHI in a Designated Record Set that Covered Entity directs or agrees to pursuant to 45 CFR § 164.526, or take other measures as necessary to satisfy Covered Entity's obligations under that section. Response window: ten (10) business days.
8. **Make available** the information required to provide an accounting of disclosures of PHI as necessary to satisfy Covered Entity's obligations under 45 CFR § 164.528. The hash-chained audit log in [analytics-service](../services/ANALYTICS_SERVICE.md) (see the `audit_events` table) is the system-of-record for this — exports are produced via the standard audit-export pipeline.
9. **Make available** to the Secretary internal practices, books, and records, including policies and procedures and PHI, relating to the Use and Disclosure of PHI for purposes of the Secretary determining Covered Entity's compliance with the HIPAA Rules.
10. **Comply, where applicable,** with the Privacy Rule's requirements that apply to Covered Entity in the performance of any obligations Business Associate has agreed to undertake on behalf of Covered Entity (e.g., responding to Individual access requests on behalf of Covered Entity).

---

## 5. Safeguards (technical, administrative, physical)

Business Associate maintains the following controls over ePHI. This list maps the standards at 45 CFR §§ 164.308–312 to the live infrastructure documented elsewhere in this repository.

### 5.1 Administrative safeguards (§ 164.308)
- **Security Management Process (§ 164.308(a)(1))** — annual risk analysis per [SOC2_READINESS.md § 5](SOC2_READINESS.md); risk-management decisions tracked in the engineering issue tracker.
- **Workforce Security (§ 164.308(a)(3))** — least-privilege access to PHI; access reviews quarterly; termination procedure removes access within one business day.
- **Information Access Management (§ 164.308(a)(4))** — role-based access control in [auth-service](../services/AUTH_SERVICE.md); per-tenant data isolation enforced by row-level security in every per-service Postgres schema.
- **Security Awareness and Training (§ 164.308(a)(5))** — required annual training for everyone with PHI access; phishing-simulation cadence quarterly.
- **Security Incident Procedures (§ 164.308(a)(6))** — § 6 of this Agreement plus the engineering on-call runbook.
- **Contingency Plan (§ 164.308(a)(7))** — nightly off-site `restic` backups + hourly ZFS snapshots per [STORAGE.md § Durability](STORAGE.md); RPO ≤ 5 min, RTO ≤ 30 min documented in the SLO doc.
- **Evaluation (§ 164.308(a)(8))** — annual third-party penetration test; SOC 2 Type II audit per [SOC2_READINESS.md](SOC2_READINESS.md).
- **Business Associate Contracts (§ 164.308(b))** — this Agreement; the downstream-Subcontractor BAAs in **Schedule B**.

### 5.2 Physical safeguards (§ 164.310)
- **Facility Access Controls** — Fyredocs operates from cloud + colocation facilities (current: `[provider / region]`) with SOC 2 / ISO 27001-certified physical access controls. Fyredocs personnel do not have physical access to underlying hardware.
- **Workstation Use & Security** — endpoint-management policy enforces full-disk encryption, screen lock, and MDM-pushed security baselines on every laptop with PHI access.
- **Device & Media Controls** — production media (storage host disks) are encrypted at rest with LUKS (§ 5.3 below). Decommissioned media is wiped per NIST 800-88 Clear / Purge.

### 5.3 Technical safeguards (§ 164.312)
- **Access Control (§ 164.312(a))** — mandatory unique user identification (UUIDv7) on every API call; emergency access procedures documented in the on-call runbook; automatic logoff via JWT expiry; encryption + decryption per § 5.3 below.
- **Audit Controls (§ 164.312(b))** — every API call against editor-service / auth-service / billing-service writes an `audit_events` row in [analytics-service](../services/ANALYTICS_SERVICE.md) with a SHA-256 hash chain (`this_hash = sha256(prev_hash || payload)`). Tampering with a historical row breaks the chain — verifiable independently.
- **Integrity (§ 164.312(c))** — content-stream edits use append-only incremental PDF updates so original bytes are preserved verbatim, including any prior digital signatures over them (see [editor-service § L1 writer](../services/EDITOR_SERVICE.md)). Document checksums are stored alongside the file path in Postgres and verified on a sampled basis.
- **Person or Entity Authentication (§ 164.312(d))** — JWT-based session auth via auth-service; mandatory MFA for Fyredocs operators with production access.
- **Transmission Security (§ 164.312(e))** — TLS 1.3 in transit (terminated at api-gateway with HSTS); internal mesh traffic over mTLS; PHI never leaves the customer's data-residency region (US-East / EU-West / AP-Southeast / AU-Sydney — enforced by the policy primitive in [`shared/residency/`](../../../shared/residency/residency.go), which `Validate()`s every per-org request against the org's assigned region. Cross-region calls return `ErrRegionMismatch` at the gateway — see **Schedule C**).
- **Encryption at Rest** — full-disk LUKS on the storage host; for ePHI-tier tenants, **per-tenant LUKS-encrypted mounts** described in [STORAGE.md § Per-tenant isolation](STORAGE.md), with per-tenant key envelopes managed in [SECRETS.md](SECRETS.md). No third-party KMS sees plaintext keys.
- **Encryption in Transit between services** — mTLS via the planned Envoy sidecar.

### 5.4 Encryption standards
ePHI is encrypted with FIPS-validated algorithms: **AES-256-GCM** at rest (LUKS + envelope-encrypted DEKs), **TLS 1.3** in transit. Key rotation cadence: yearly minimum for KEKs; per-document DEKs are rotated on every revision.

---

## 6. Reporting and breach notification

### 6.1 Cadence
| Event | Notice to Covered Entity | Form of notice |
|---|---|---|
| **Breach of Unsecured PHI** (45 CFR § 164.410) | Without unreasonable delay; in no case later than **sixty (60) calendar days** after Discovery. | Written notice to the Covered Entity's designated contact (Schedule C). |
| **Security Incident** (other than Unsuccessful Security Incidents) | Without unreasonable delay; in no case later than **ten (10) business days** after Discovery. | Written notice (same channel). |
| **Unsuccessful Security Incidents** (port scans, blocked-at-edge attempts) | Aggregated **monthly** in the standard security report; no per-event notice. | Section of monthly report. |
| **Subpoena or compelled Disclosure** of PHI | Promptly, and where lawful, before producing the PHI. | Written notice (same channel). |

### 6.2 Content of breach notice
At minimum: identity of each Individual whose PHI was or is reasonably believed to have been affected; the date of the Breach and the date of Discovery; the types of PHI involved; remedial actions Business Associate has taken; and any other information Covered Entity must include in its own notification to Individuals under 45 CFR § 164.404.

### 6.3 Cooperation
Business Associate will reasonably assist Covered Entity in meeting Covered Entity's notification obligations under 45 CFR §§ 164.404, 164.406, 164.408. Costs of notification are allocated per § 10 below.

---

## 7. Subcontractors

Business Associate will not allow any Subcontractor to Create, Receive, Maintain, or Transmit PHI without first entering into a written agreement with that Subcontractor that contains restrictions and conditions at least as protective as those in this Agreement.

**Schedule B** lists the current Subcontractors and the categories of PHI each may access. Business Associate will give Covered Entity written notice of any proposed change to Schedule B at least **thirty (30) calendar days** before the change becomes effective; Covered Entity may object to the change before it takes effect, in which case the Parties will negotiate in good faith or, failing agreement, the Covered Entity may terminate under § 8.4(b).

---

## 8. Term and termination

### 8.1 Term
This Agreement is effective as of the Effective Date and continues until the Underlying Agreement terminates or this Agreement is otherwise terminated, whichever is earlier.

### 8.2 Termination for cause by Covered Entity
Upon Covered Entity's knowledge of a material breach by Business Associate, Covered Entity will either:
1. provide Business Associate an opportunity to cure the breach or end the violation, and terminate this Agreement and the Underlying Agreement if Business Associate does not cure the breach or end the violation within **thirty (30) calendar days**; or
2. immediately terminate this Agreement and the Underlying Agreement if cure is not possible.

### 8.3 Effect of termination — return or destruction of PHI
Upon termination of this Agreement, Business Associate will, **as feasible**, return to Covered Entity or destroy all PHI received from, or created or received on behalf of, Covered Entity that Business Associate still maintains in any form. If return or destruction is infeasible, Business Associate will: (a) extend the protections of this Agreement to such PHI for as long as Business Associate maintains it; and (b) limit further Uses and Disclosures of such PHI to those purposes that make return or destruction infeasible.

Destruction of ePHI is performed by cryptographic shredding — the per-tenant LUKS key is destroyed, making the on-disk ciphertext permanently unrecoverable. Per [STORAGE.md § Per-tenant isolation](STORAGE.md), this is a single key-store operation rather than a per-file overwrite.

### 8.4 Survival
Sections **2**, **3.2(3)**, **6**, **8.3**, **9**, **10**, and **11** survive termination of this Agreement.

---

## 9. Confidentiality of this Agreement

The Parties will treat this Agreement and its Schedules as Confidential Information of both Parties under the Underlying Agreement, subject to disclosure to auditors, regulators, the Secretary, and as Required by Law.

---

## 10. Indemnification and limitation of liability

> **TODO-LEGAL** — concrete liability cap, mutual-vs-one-way indemnification scope, and carve-outs (gross negligence, willful misconduct, IP infringement) are deal-specific. The Parties insert the negotiated terms here. The default position Fyredocs offers in this template is:
>
> - Mutual indemnity for direct damages caused by either Party's material breach of HIPAA + this Agreement, capped at the greater of (i) **fees paid under the Underlying Agreement in the prior twelve (12) months** and (ii) **the limits of Business Associate's cyber-liability insurance** (currently `[$X,000,000]`).
> - No indemnification for indirect, incidental, consequential, special, or punitive damages, except for breach-notification costs imposed on Covered Entity by 45 CFR § 164.404 that are directly attributable to Business Associate's material breach.
> - Mandatory cooperation with insurance carriers + counsel.

---

## 11. General provisions

### 11.1 Amendment
The Parties agree to take such action as is necessary to amend this Agreement from time to time as is necessary for compliance with the requirements of the HIPAA Rules and any other applicable law. No other amendment is effective unless in a writing signed by both Parties.

### 11.2 Interpretation
Ambiguity in this Agreement is resolved to permit compliance with the HIPAA Rules.

### 11.3 Regulatory references
References to a section of the HIPAA Rules mean the section in effect or as amended at the relevant time.

### 11.4 No third-party beneficiaries
Nothing in this Agreement is intended to confer, nor does it confer, any rights on any third party.

### 11.5 Notices
All notices required by this Agreement are in writing and delivered to the addresses in **Schedule C**, either by hand, by national overnight courier, or by certified mail (return receipt requested). Email notice is acceptable for § 6 reporting events.

### 11.6 Governing law
This Agreement is governed by the laws of `[Delaware / state TBD]`, excluding its choice-of-law rules, and to the extent applicable, by the HIPAA Rules. Nothing in this section subjects Business Associate to the jurisdiction of any state regulator that would not otherwise have jurisdiction.

### 11.7 Counterparts; electronic signatures
This Agreement may be executed in counterparts (including via electronic signature platforms that produce a tamper-evident audit trail), each of which is an original and which together constitute one instrument.

---

## 12. Signatures

**Covered Entity:**

| | |
|---|---|
| Signature: | __________________________________ |
| Name: | `[Authorised signatory]` |
| Title: | `[Title]` |
| Date: | `[YYYY-MM-DD]` |

**Business Associate (Fyredocs):**

| | |
|---|---|
| Signature: | __________________________________ |
| Name: | `[Authorised signatory]` |
| Title: | `[Title]` |
| Date: | `[YYYY-MM-DD]` |

---

## Schedule A — Permitted purposes

Business Associate may Create, Receive, Maintain, and Transmit PHI for the following purposes, each tied to a specific feature of the Underlying Agreement:

| Purpose | Underlying Agreement reference | Fyredocs services involved |
|---|---|---|
| Storing PDF documents on behalf of Covered Entity | `[MSA § X / Order Form line Y]` | [editor-service](../services/EDITOR_SERVICE.md) + the storage layer described in [STORAGE.md](STORAGE.md) |
| Performing user-initiated edits (rotate, redact, table.cell.edit, text.replace, etc.) | `[MSA § X]` | editor-service |
| Producing thumbnails + OCR text for search | `[MSA § X]` | [optimize-pdf](../services/OPTIMIZE_PDF.md) |
| Routing notification events to Covered Entity's email / webhook / Slack / push targets | `[MSA § X]` | [notify-service](../services/NOTIFY_SERVICE.md) |
| Maintaining the hash-chained audit log of PHI access events | `[MSA § X]` | [analytics-service](../services/ANALYTICS_SERVICE.md) |
| Generating tenant-scoped usage rollups for billing | `[MSA § X]` | [billing-service](../services/BILLING_SERVICE.md) |

Any Use or Disclosure of PHI outside the above purposes is outside this Agreement and requires Covered Entity's written consent.

---

## Schedule B — Permitted Subcontractors

The Subcontractors below have signed BAAs with Business Associate. Adding a Subcontractor follows the § 7 notice-and-objection process.

| Subcontractor | Category of access | Geographic processing region | BAA on file (date) |
|---|---|---|---|
| `[Cloud / colocation provider]` | Infrastructure (no application-level PHI access; encrypted block storage only) | `[region]` | `[YYYY-MM-DD]` |
| `[Backup provider — Backblaze B2 or equivalent]` | Encrypted nightly off-site backups (ciphertext only; provider has no keys) | `[region]` | `[YYYY-MM-DD]` |
| `[Email gateway — Postmark / SES / TBD]` | Outbound transactional email (subject lines + recipient addresses MAY constitute PHI when an Individual is the recipient — minimum-necessary applies) | `[region]` | `[YYYY-MM-DD]` |
| `[Error-monitoring — Sentry / TBD]` | Stack traces and error metadata (Fyredocs SDKs are configured to strip PHI from error contexts before they leave the environment — see the error-stripping policy in [SECRETS.md](SECRETS.md)) | `[region]` | `[YYYY-MM-DD]` |
| `[Expo Push (when push notifications are enabled)]` | Push payload (the channel forwards Title/Body verbatim — Covered Entity is responsible for not putting PHI in titles or bodies; data field is forwarded but should be opaque) | Global | `[YYYY-MM-DD]` |

Subcontractors that explicitly do NOT receive PHI under any circumstance: `[Stripe billing — payment cards only, no PHI]`, `[Slack — workspace messages routed by Covered Entity's own webhook URL; PHI prohibited from payloads by Covered Entity policy]`.

---

## Schedule C — Notice contacts

**Covered Entity:**
- Privacy Officer: `[Name, title, email, phone]`
- Security Officer: `[Name, title, email, phone]`
- Legal / contract notices: `[Address]`

**Business Associate (Fyredocs):**
- Security incident reporting: `security@fyredocs.com` (24×7 on-call)
- Privacy Officer: `privacy@fyredocs.com`
- Legal / contract notices: `legal@fyredocs.com` and `[mailing address]`
- Data-residency questions: `[customer-success email]`

A Security Incident or Breach reported via email to `security@fyredocs.com` is acknowledged within four (4) business hours; substantive response per the § 6 cadence.

---

## Internal-engineering notes (delete before sending to a prospect)

- This template is held in source control specifically so that any change to a referenced control surface (storage layout, audit-log schema, encryption posture) forces a paired update here. Reviewers: if you touch [STORAGE.md](STORAGE.md), [SECRETS.md](SECRETS.md), [ANALYTICS_SERVICE.md](../services/ANALYTICS_SERVICE.md), or [AUTH_SERVICE.md](../services/AUTH_SERVICE.md) and a claim in **§ 5 Safeguards** no longer holds, fix this file in the same PR.
- The 60-day Breach Notification window in § 6.1 is the statutory maximum (45 CFR § 164.410) — we ship a 60-day commitment, not a shorter one, to avoid contractually binding ourselves to a more aggressive window than HIPAA itself requires. If a Covered Entity demands shorter (e.g., 30 days), legal handles via amendment to § 6.1.
- The cryptographic-shredding language in § 8.3 only works for tenants on per-tenant LUKS mounts. Tenants on the shared `/files/` mount cannot get this property — for those tenants, § 8.3 falls back to file-level deletion + cleanup-worker sweep. Sales must qualify customers into the per-tenant-mount tier before signing this template as-is.
- Schedule B is descriptive of CURRENT state. Adding a Subcontractor that gets PHI access (e.g., a new OCR vendor) requires:
  1. A signed BAA from the new vendor.
  2. § 7 notice to every Covered Entity with an active BAA.
  3. Update to this Schedule B in source control.
- The `[$X,000,000]` cyber-liability number in § 10 must match the certificate currently held with our broker. Check before sending.
