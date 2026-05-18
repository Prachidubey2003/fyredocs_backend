# Deployment SQL

One-shot SQL scripts that wrap operational concerns the runtime services can't apply to themselves (typically because the script is **revoking** privileges from the role the service runs as — auto-applying would be self-defeating).

Apply order matters. Run each script under a Postgres role that already has the necessary admin rights (typically the deployment / migration role, NOT the per-service role).

## Scripts

### `audit_security.sql`

Hardens the `audit_events` table (analytics-service) against tampering. Three concentric defenses:

1. **`BEFORE UPDATE` trigger** — raises an exception when any chain-input column (seq, actor, action, resource, metadata, prev_hash, occurred_at, created_at) is mutated. The `hash` column stays writable because `AppendAudit` uses an INSERT → UPDATE(hash) pattern (BIGSERIAL `seq` isn't known until INSERT returns).
2. **`BEFORE DELETE` + `BEFORE TRUNCATE` triggers** — append-only enforcement at the DB layer. Bypasses every connection that isn't using `session_replication_role = 'replica'`.
3. **Role-level REVOKE/GRANT** — the analytics-service writer credential gets `SELECT, INSERT, UPDATE(hash)` only. Stops application-level credential abuse.

The hash chain in [`analytics-service/internal/audit/chain.go`](../../analytics-service/internal/audit/chain.go) is the *detective* control. The triggers + REVOKE are the *preventive* control.

#### When to apply

- **First deploy:** after analytics-service has started for the first time and AutoMigrate has created the `audit_events` table. Apply this script before the service handles user-facing traffic so no audit row is ever written without the trigger guards.
- **Schema migrations:** if a future migration touches `audit_events`, re-run this script. It's idempotent (drops + recreates the triggers).

#### How to apply

```bash
psql "$DEPLOY_DSN" \
  --set ON_ERROR_STOP=on \
  --set analytics_role="${ANALYTICS_DB_ROLE:-fyredocs_analytics}" \
  -f deployment/sql/audit_security.sql
```

`$DEPLOY_DSN` is your deploy-role DSN; `$ANALYTICS_DB_ROLE` is the role the analytics-service connects as in production.

#### Verifying the hardening

After applying, the smoke test at the bottom of the script (in a SQL comment) walks through three tamper attempts — UPDATE, DELETE, TRUNCATE — each of which should raise. Run them once under the `analytics_role` to confirm.

For ongoing assurance:

- `GET /internal/v1/audit/verify` — re-walks the chain end-to-end. Run nightly via cron + on-call paging on `ok=false`.
- Compare row count + max(seq) before/after every backup-restore. A drop is a chain break the verifier will then localise.

#### Rolling back

In an emergency (e.g., a forensic team needs to copy rows to a quarantine table) the triggers can be temporarily disabled:

```sql
ALTER TABLE audit_events DISABLE TRIGGER audit_events_tamper_block;
ALTER TABLE audit_events DISABLE TRIGGER audit_events_delete_block;
-- ... do the work ...
ALTER TABLE audit_events ENABLE TRIGGER audit_events_tamper_block;
ALTER TABLE audit_events ENABLE TRIGGER audit_events_delete_block;
```

Disabling triggers is logged in Postgres's standard event audit (when configured). The hash chain ALSO catches any tamper that happened during the disable window because the chain math is independent of the triggers.
