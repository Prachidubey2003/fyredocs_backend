#!/usr/bin/env bash
# zfs-snapshot.sh — hourly ZFS snapshot of the /files dataset.
#
# Per plan §4.4.4: hourly snapshots provide instant rollback if a runaway
# cleanup-worker or a buggy migration walks the tree. Snapshots are local,
# free, and atomic.
#
# Run on the storage host (the one whose ZFS pool backs /files). The
# fyredocs services elsewhere do not need this script.
#
# Environment:
#   ZFS_DATASET           - dataset path, e.g. tank/files
#   KEEP_HOURLY           - hourly snapshots to retain, default 24
#   KEEP_DAILY            - daily snapshots to retain, default 7

set -euo pipefail

ZFS_DATASET="${ZFS_DATASET:-tank/files}"
KEEP_HOURLY="${KEEP_HOURLY:-24}"
KEEP_DAILY="${KEEP_DAILY:-7}"

if ! command -v zfs >/dev/null; then
  echo "zfs(8) not found on this host" >&2; exit 2
fi

if ! zfs list "$ZFS_DATASET" >/dev/null 2>&1; then
  echo "ZFS dataset $ZFS_DATASET does not exist" >&2; exit 2
fi

now=$(date -u +%Y%m%dT%H%M%SZ)
snap="${ZFS_DATASET}@auto-hourly-${now}"

echo "[zfs-snapshot] creating $snap"
zfs snapshot "$snap"

echo "[zfs-snapshot] pruning hourly (keep ${KEEP_HOURLY})"
zfs list -H -t snapshot -o name "$ZFS_DATASET" \
  | awk -F@ -v prefix='auto-hourly-' '$2 ~ "^"prefix' \
  | sort -r \
  | tail -n +"$((KEEP_HOURLY + 1))" \
  | while read -r old; do
      echo "  destroy $old"
      zfs destroy "$old"
    done

# Promote one snapshot per day at the 00:* slot to "daily-" so daily pruning
# is independent from hourly. Idempotent: rename only if a daily for this
# date doesn't already exist.
hour=$(date -u +%H)
if [[ "$hour" == "00" ]]; then
  date_tag=$(date -u +%Y%m%d)
  daily_name="${ZFS_DATASET}@auto-daily-${date_tag}"
  if ! zfs list "$daily_name" >/dev/null 2>&1; then
    echo "[zfs-snapshot] promoting today's 00 snapshot to daily ($daily_name)"
    zfs rename "$snap" "$daily_name"
  fi

  echo "[zfs-snapshot] pruning daily (keep ${KEEP_DAILY})"
  zfs list -H -t snapshot -o name "$ZFS_DATASET" \
    | awk -F@ -v prefix='auto-daily-' '$2 ~ "^"prefix' \
    | sort -r \
    | tail -n +"$((KEEP_DAILY + 1))" \
    | while read -r old; do
        echo "  destroy $old"
        zfs destroy "$old"
      done
fi

echo "[zfs-snapshot] done"
