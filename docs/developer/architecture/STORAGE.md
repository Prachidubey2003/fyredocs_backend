# Storage architecture

The Fyredocs backend keeps every uploaded file, every worker output, and
every signed artifact on a **local POSIX filesystem mounted at `/files/`**.
This document captures the layout, integrity guarantees, scaling path, and
operational tooling for that layer.

It is the implementation companion to plan section [§4.4](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md) — "Storage strategy — keep current local-filesystem approach".

## Why local FS (and not S3 / R2)

- Workers already write to `/files/` — no rewrite, no migration risk.
- Zero vendor lock-in; zero new bill; zero new failure mode.
- Scales linearly with mount size; NFS / CephFS gets us very far.
- We migrate to a managed object store only when measurable triggers fire (§ "Evolution path" below) — no cargo-culted "should-be-on-S3".

## Current state

- **Single host.** Workers and the API gateway run on the same machine; `/files/` is a host-local directory bind-mounted into worker containers via Docker Compose ([`deployment/docker-compose.yml`](../../../deployment/docker-compose.yml)).
- **No per-tenant partitioning yet.** Today files land in `uploads/<uploadId>/`, `outputs/<jobId>/`, etc. The path-builder helpers in [`shared/storage/paths.go`](../../../shared/storage/paths.go) define the per-owner convention workers will adopt incrementally.
- **No checksum coverage yet.** The `file_metadata.sha256_hash` column now exists in every service's model (Phase 0), and [`shared/storage/hash.go`](../../../shared/storage/hash.go) provides the helpers. Wiring the *computation* into worker write paths is a separate follow-up — see "Adoption sequence" below.

## Target layout (`§4.4.3`)

```
/files/
  users/<user_id>/
    uploads/<upload_id>/         # chunked-upload chunks + assembled file
      000000.part
      000001.part
      <fileName>
    jobs/<job_id>/
      input/<fileName>
      scratch/                   # worker intermediates; excluded from backup
      output/<fileName>          # final artifact returned to client
  guests/<job_id>/               # guests have no stable identity; job_id is the partition
    uploads/<upload_id>/...
    jobs/<job_id>/...
  tmp/                           # in-flight things that don't belong to anyone yet
    <upload_id>/                 # transient pre-assembly chunks (cleanup-worker)
```

Helpers in [`shared/storage/paths.go`](../../../shared/storage/paths.go):

| Helper | Returns |
|---|---|
| `OwnerForUser(userID)` | `users/<userID>/` |
| `OwnerForGuest(jobID)` | `guests/<jobID>/` |
| `OwnerFor(userID *uuid.UUID, jobID)` | dispatches based on nil-ness |
| `UploadChunkPath(base, owner, uploadID, idx)` | `<base>/<owner>/uploads/<uploadID>/000000.part` |
| `UploadAssembledPath(base, owner, uploadID, name)` | `<base>/<owner>/uploads/<uploadID>/<name>` |
| `JobInputPath(base, owner, jobID, name)` | `<base>/<owner>/jobs/<jobID>/input/<name>` |
| `JobScratchDir(base, owner, jobID)` | `<base>/<owner>/jobs/<jobID>/scratch` |
| `JobOutputPath(base, owner, jobID, name)` | `<base>/<owner>/jobs/<jobID>/output/<name>` |
| `SafeFileName(s)` | base name only; prevents path traversal |

All file names passed to the builders are sanitized via `SafeFileName` so an attacker-supplied `../../../etc/passwd` becomes `passwd` and stays inside the owner directory.

## Integrity — SHA-256 checksums

### Schema

Every service's `FileMetadata` model now carries a nullable
`sha256_hash CHAR(64)` column (lowercase hex). GORM's `AutoMigrate` adds the
column on next startup. The column is nullable so legacy rows from before
this change remain valid until they are backfilled or expire.

```go
type FileMetadata struct {
    // ...
    Path       string  `gorm:"type:text;not null"`
    Sha256Hash *string `gorm:"type:char(64);column:sha256_hash"`
    SizeBytes  int64
    // ...
}
```

### Helpers

`shared/storage/hash.go` exposes three primitives — all pure functions over
`io.Reader` / file paths:

- `HashStream(r io.Reader) (hex string, n int64, err error)` — one pass.
- `HashFile(path string) (hex string, n int64, err error)` — open + hash.
- `Verify(path, expectedHex string) error` — case-insensitive; returns `ErrChecksumMismatch` on divergence.
- `TeeHasher(r io.Reader) (io.Reader, *Hasher)` — for the common case where bytes are already being copied somewhere; one pass, no second I/O.

### Adoption sequence (worker wiring — follow-up)

This Phase 0 change introduces the schema column and the helpers. Wiring the *computation* into the workers is a per-service follow-up:

| Service | Hook point (when adopted) |
|---|---|
| `job-service` | [`handlers/uploads.go`](../../../job-service/handlers/uploads.go) chunk assembly: wrap the destination write with `TeeHasher`, persist `hash` into the `FileMetadata` row created at completion. |
| `job-service` | [`handlers/jobs.go`](../../../job-service/handlers/jobs.go) `c.SaveUploadedFile` path: post-write `HashFile`. |
| `optimize-pdf` | [`processing/processing.go`](../../../optimize-pdf/processing/processing.go) output write: `TeeHasher` around the copy into `outputs/...`. |
| `organize-pdf` | Same pattern at each `copyFile`. |
| `convert-from-pdf` | Same pattern at each output close. |
| `convert-to-pdf` | Same pattern at each output close. |

Until adoption lands, `sha256_hash` remains NULL for new rows — that is acceptable, but the periodic verification script (below) only checks rows where it is set.

### Verification

`deployment/storage/verify-checksums.sh` samples N random rows where `sha256_hash IS NOT NULL`, recomputes the digest, and exits non-zero on any mismatch. Schedule it via a daily systemd timer alongside the snapshot/backup timers. Any failure is treated as a **data-integrity incident** — quarantine the file, alert oncall, restore from the most recent verified snapshot or restic backup.

## Backups and snapshots

Two independent layers, two different failure modes covered.

### ZFS snapshots — fast local rollback

- **What:** ZFS atomic snapshots of the `/files/` dataset.
- **Cadence:** hourly via [`zfs-snapshot.timer`](../../../deployment/storage/zfs-snapshot.timer).
- **Retention:** 24 hourly + 7 daily. Daily snapshots are promoted from the `00:07` slot.
- **Use case:** runaway `cleanup-worker`, bad migration, accidental `rm -rf`. Rollback is `zfs rollback` — seconds, no network.
- **Limitations:** local only — does **not** protect against host loss.

### Restic — off-site disaster recovery

- **What:** `restic backup` to a remote bucket (Backblaze B2 / S3-compatible).
- **Cadence:** nightly at 02:30 via [`restic-backup.timer`](../../../deployment/storage/restic-backup.timer).
- **Retention:** 7 daily / 4 weekly / 12 monthly (env-tunable).
- **Excludes:** `tmp/`, `scratch/` (transient by design).
- **Integrity:** every run finishes with `restic check --read-data-subset=5%`.
- **Use case:** host loss, ransomware, region outage.

The encryption password and bucket credentials live in `/etc/fyredocs/restic.env` (mode 0400, owner root). Never committed.

### Operations runbook

The install / restore / rollback recipes live in [`deployment/storage/README.md`](../../../deployment/storage/README.md).

## Evolution path

We do not jump to managed object storage until measurable triggers fire.

| Stage | Topology | Trigger to advance |
|---|---|---|
| **Now** | Single host, single Docker volume | (current) |
| **Stage 1** | Multi-host with shared volume (NFS / CephFS / GlusterFS) | > 1 host needed for worker capacity |
| **Stage 2** | Sharded local FS by `doc_id` hash + fs-router sidecar | NFS p99 > target on > ~30 workers |
| **Stage 3** | Self-hosted S3-compatible (MinIO / SeaweedFS / Garage) | Multi-region durability or object-versioning becomes a requirement |

Stages 1–3 reuse the same path conventions and SHA-256 invariants — the change is the storage backend, not the API or the data model.

## Per-tenant isolation (hard mode)

For regulated tenants (HIPAA / FINRA), §4.4.5 calls for a **dedicated LUKS-encrypted mount per tenant** (`/files-{tenant}/`). The path-builder helpers are agnostic about whether `<owner>` lives under `/files/` or `/files-{tenant}/` — the worker config simply points at a different base. We add this knob when the first such tenant signs.

## Threat model

| Risk | Mitigation |
|---|---|
| Disk failure | ZFS RAID-Z + replicated to ≥ 2 hosts (Stage 1+). Restic off-site nightly. |
| Bit rot | ZFS scrub (weekly, separately scheduled). SHA-256 verification samples. |
| Accidental delete | ZFS hourly snapshots → instant rollback. Restic 7-day retention. |
| Path traversal in user-supplied filenames | `SafeFileName` strips separators; helpers always join under the owner dir. |
| Tampering in flight (man-in-the-middle on chunk upload) | TLS 1.3 at gateway + future per-chunk MAC (Phase 5 enterprise). |
| Tampering at rest | Periodic SHA-256 verification + ZFS checksumming on read. |
| Worker writes the wrong tenant's data | `Owner` struct constructed from authenticated identity at request time; helpers never accept a raw user-controlled path component as a directory. |

## Compliance mapping

| Control | Evidence |
|---|---|
| SOC2 CC6.7 (data transmission and disposal) | restic encrypted backups, ZFS dataset destroy on tenant offboarding |
| SOC2 CC7.3 (security incidents) | `verify-checksums.sh` failure → pager → incident |
| HIPAA §164.312(c)(1) (integrity) | sha256_hash column + verification job |
| HIPAA §164.308(a)(7)(ii)(D) (testing and revision of backup procedures) | quarterly DR drill (restic restore → rsync dry-run → review) |
| GDPR Art. 32 (security of processing) | encryption at rest (LUKS or filesystem-level), backup encryption (restic native), per-tenant isolation option |

## Files added in this change (Phase 0)

- [`shared/storage/doc.go`](../../../shared/storage/doc.go)
- [`shared/storage/hash.go`](../../../shared/storage/hash.go)
- [`shared/storage/hash_test.go`](../../../shared/storage/hash_test.go)
- [`shared/storage/paths.go`](../../../shared/storage/paths.go)
- [`shared/storage/paths_test.go`](../../../shared/storage/paths_test.go)
- [`deployment/storage/restic-backup.sh`](../../../deployment/storage/restic-backup.sh) + `.service` + `.timer`
- [`deployment/storage/zfs-snapshot.sh`](../../../deployment/storage/zfs-snapshot.sh) + `.service` + `.timer`
- [`deployment/storage/verify-checksums.sh`](../../../deployment/storage/verify-checksums.sh)
- [`deployment/storage/README.md`](../../../deployment/storage/README.md)
- `Sha256Hash *string` column added to `FileMetadata` in: [job-service](../../../job-service/internal/models/job.go), [optimize-pdf](../../../optimize-pdf/internal/models/job.go), [organize-pdf](../../../organize-pdf/internal/models/job.go), [convert-from-pdf](../../../convert-from-pdf/internal/models/job.go), [convert-to-pdf](../../../convert-to-pdf/internal/models/job.go), [cleanup-worker](../../../cleanup-worker/internal/models/job.go).

## Next (follow-up work — NOT in this change)

- Wire `TeeHasher` into each worker's write path (see "Adoption sequence" above).
- Migrate existing legacy paths (`uploads/<id>/...`) to the per-owner layout via a one-time migrator or lazy-write-through.
- Add a metrics counter `fyredocs_storage_checksum_failures_total` and a Prometheus alert.
- Add the `verify-checksums` systemd timer to the install playbook.
