# Storage operations

Operational scripts and systemd units for the `/files/` local-filesystem
layer described in [`docs/developer/architecture/STORAGE.md`](../../docs/developer/architecture/STORAGE.md).

These files are *not* deployed by `deploy.sh`. They are intended to be installed on the **storage host** (the bare-metal/cloud VM that owns the `/files/` mount).

## Files

| File | Purpose |
|---|---|
| `restic-backup.sh` | Nightly off-site backup driver. Excludes `tmp/` and `scratch/`. Self-prunes per retention policy. |
| `restic-backup.service` | systemd one-shot unit invoking the script with hardened sandbox. |
| `restic-backup.timer` | Fires the unit nightly at 02:30 local time (+ 30m jitter). |
| `zfs-snapshot.sh` | Hourly ZFS snapshot driver. Promotes one snapshot per day to "daily-" and prunes old ones. |
| `zfs-snapshot.service` | systemd one-shot for the snapshot script. |
| `zfs-snapshot.timer` | Fires hourly at :07. |
| `verify-checksums.sh` | Random-sample verification of `file_metadata.sha256_hash` against on-disk bytes. Exits non-zero on any mismatch — pages oncall. |

## Install (storage host)

```bash
# 1. Copy scripts into PATH
sudo install -m 0755 -t /usr/local/bin \
  restic-backup.sh zfs-snapshot.sh verify-checksums.sh

# 2. Copy unit + timer files
sudo install -m 0644 -t /etc/systemd/system \
  restic-backup.service restic-backup.timer \
  zfs-snapshot.service zfs-snapshot.timer

# 3. Create the secret env file restic needs (mode 0400, owner root).
sudo install -m 0400 /dev/stdin /etc/fyredocs/restic.env <<'EOF'
RESTIC_REPOSITORY=b2:my-bucket:fyredocs/files
RESTIC_PASSWORD_FILE=/etc/fyredocs/restic.pw
B2_ACCOUNT_ID=...
B2_ACCOUNT_KEY=...
EOF

# 4. Enable and start
sudo systemctl daemon-reload
sudo systemctl enable --now restic-backup.timer zfs-snapshot.timer

# 5. Verify
systemctl list-timers | grep -E 'restic-backup|zfs-snapshot'
```

The `verify-checksums.sh` script is intended to be scheduled separately
(e.g., from the apps host that already has `DATABASE_URL`) — a sibling timer
can be added once the worker-side checksum write path lands.

## Restoring

```bash
# List snapshots
restic snapshots

# Restore a specific snapshot to a temp dir, then rsync into place after review.
sudo restic restore <snapshot-id> --target /var/tmp/restore
sudo rsync -a --dry-run /var/tmp/restore/files/ /files/  # dry-run first!
```

For ZFS rollback:
```bash
zfs list -t snapshot | grep auto-hourly | head
sudo zfs rollback tank/files@auto-hourly-20260513T030000Z
```

## Local development

Don't run these scripts locally. They expect `restic`, `zfs(8)`, and a
production env file. Dev work uses the bind-mounted `files/` directory
inside the repo.
