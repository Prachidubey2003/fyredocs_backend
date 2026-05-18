#!/usr/bin/env bash
# restic-backup.sh — nightly off-site backup of /files/.
#
# Per plan §4.4.4: nightly restic backup is the disaster-recovery layer.
# Runtime never reads from this. Restore is operator-only via `restic restore`.
#
# Required environment:
#   RESTIC_REPOSITORY     - e.g. b2:bucket:fyredocs/files
#   RESTIC_PASSWORD_FILE  - path to a 0400 file with the repo password
#   B2_ACCOUNT_ID / B2_ACCOUNT_KEY (or equivalent for your backend)
#
# Optional:
#   FILES_ROOT            - root to back up, default /files
#   RESTIC_TAG            - tag attached to the snapshot, default $(hostname)
#   FORGET_KEEP_DAILY     - daily retention, default 7
#   FORGET_KEEP_WEEKLY    - weekly retention, default 4
#   FORGET_KEEP_MONTHLY   - monthly retention, default 12

set -euo pipefail

FILES_ROOT="${FILES_ROOT:-/files}"
RESTIC_TAG="${RESTIC_TAG:-$(hostname)}"
FORGET_KEEP_DAILY="${FORGET_KEEP_DAILY:-7}"
FORGET_KEEP_WEEKLY="${FORGET_KEEP_WEEKLY:-4}"
FORGET_KEEP_MONTHLY="${FORGET_KEEP_MONTHLY:-12}"

if [[ -z "${RESTIC_REPOSITORY:-}" ]]; then
  echo "RESTIC_REPOSITORY not set" >&2; exit 2
fi
if [[ -z "${RESTIC_PASSWORD_FILE:-}" ]]; then
  echo "RESTIC_PASSWORD_FILE not set" >&2; exit 2
fi
if [[ ! -d "$FILES_ROOT" ]]; then
  echo "FILES_ROOT=$FILES_ROOT does not exist" >&2; exit 2
fi

echo "[restic-backup] $(date -Iseconds) starting backup of $FILES_ROOT (tag=$RESTIC_TAG)"

# Initialize the repo on first run; ignore failures (already initialized).
restic snapshots >/dev/null 2>&1 || restic init

# Skip in-flight chunk dirs (they churn fast and aren't durable state).
restic backup \
  --tag "$RESTIC_TAG" \
  --exclude "*/tmp/*" \
  --exclude "*/scratch/*" \
  --one-file-system \
  --verbose=1 \
  "$FILES_ROOT"

echo "[restic-backup] pruning per retention policy"
restic forget --prune \
  --keep-daily "$FORGET_KEEP_DAILY" \
  --keep-weekly "$FORGET_KEEP_WEEKLY" \
  --keep-monthly "$FORGET_KEEP_MONTHLY"

echo "[restic-backup] verifying repository integrity (--read-data-subset=5%)"
restic check --read-data-subset=5%

echo "[restic-backup] $(date -Iseconds) done"
