#!/usr/bin/env bash
# verify-checksums.sh — sample-verify file_metadata.sha256_hash against
# the bytes actually on disk.
#
# Per plan §4.4.4: "Checksum (SHA-256) stored in Postgres alongside file path;
# verified on every read in CI smoke and on a sampled basis in production."
#
# This script picks a random N rows from file_metadata where sha256_hash IS
# NOT NULL, recomputes the digest, and exits non-zero on any mismatch.
# Schedule it via a systemd timer (daily) and route failures to oncall.
#
# Environment:
#   DATABASE_URL          - Postgres DSN (psql-compatible)
#   FILES_ROOT            - mount point of /files, default /files
#   SAMPLE_SIZE           - rows to verify per run, default 200
#   PSQL                  - psql binary, default psql

set -euo pipefail

DATABASE_URL="${DATABASE_URL:?DATABASE_URL must be set}"
FILES_ROOT="${FILES_ROOT:-/files}"
SAMPLE_SIZE="${SAMPLE_SIZE:-200}"
PSQL="${PSQL:-psql}"

if ! command -v sha256sum >/dev/null; then
  echo "sha256sum(1) not found" >&2; exit 2
fi

# TABLESAMPLE SYSTEM gives us a cheap probabilistic sample; we then LIMIT to
# the exact count so the budget is predictable.
read -r -d '' QUERY <<SQL || true
SELECT id, path, sha256_hash
  FROM file_metadata TABLESAMPLE SYSTEM (5)
 WHERE sha256_hash IS NOT NULL
 ORDER BY random()
 LIMIT ${SAMPLE_SIZE};
SQL

echo "[verify-checksums] $(date -Iseconds) sampling up to ${SAMPLE_SIZE} rows"

fail=0
total=0

# psql tab-separated output, suppress headers + footer.
while IFS=$'\t' read -r id rel_path expected; do
  [[ -z "${id:-}" ]] && continue
  total=$((total + 1))
  abs="${FILES_ROOT}/${rel_path#/}"
  if [[ ! -f "$abs" ]]; then
    echo "MISSING $id $abs"
    fail=$((fail + 1))
    continue
  fi
  got=$(sha256sum "$abs" | awk '{print $1}')
  if [[ "$got" != "$expected" ]]; then
    echo "MISMATCH $id path=$abs want=$expected got=$got"
    fail=$((fail + 1))
  fi
done < <("$PSQL" -At -F$'\t' "$DATABASE_URL" -c "$QUERY")

echo "[verify-checksums] verified=${total} failures=${fail}"

if (( fail > 0 )); then
  echo "[verify-checksums] FAILED — open an incident: storage integrity breach" >&2
  exit 1
fi
