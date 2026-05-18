-- audit_security.sql
--
-- Hardens the `audit_events` table against tampering. Apply this
-- once, AFTER analytics-service has started and AutoMigrate'd the
-- table for the first time. Idempotent — re-running is safe.
--
-- Defense layers (both required; either alone is insufficient):
--
--   1. REVOKE/GRANT (role-level): the analytics-service DB role
--      can INSERT new rows and UPDATE the `hash` column (the
--      INSERT → UPDATE(hash) pattern in AppendAudit needs this
--      because BIGSERIAL `seq` isn't known until the INSERT
--      returns). UPDATE on any other column + all DELETE is
--      revoked. Stops an attacker who got hold of the
--      analytics-service credentials from rewriting history at
--      the application level.
--
--   2. Triggers (database-level): BEFORE UPDATE/DELETE triggers
--      raise an exception on any tamper attempt — even from
--      DBAs / superusers — unless they explicitly disable
--      `session_replication_role` (which itself is auditable in
--      `pg_stat_activity`). The hash chain (audit/chain.go) is
--      the *detective* control that catches anyone who DID
--      bypass these.
--
-- Usage:
--
--   psql "$DATABASE_URL" \
--     --set ON_ERROR_STOP=on \
--     --set analytics_role="${ANALYTICS_DB_ROLE:-fyredocs_analytics}" \
--     -f deployment/sql/audit_security.sql
--
-- The `analytics_role` variable defaults to `fyredocs_analytics`;
-- override it via --set when your deployment uses a different
-- role name.
--
-- Run order matters: the triggers below are owned by the
-- session that creates them (typically the deploy/admin role).
-- Apply this script under an admin DSN, then point
-- analytics-service at its own service-role DSN.

\set ON_ERROR_STOP on
\set analytics_role :'analytics_role'

BEGIN;

-- ---------------------------------------------------------------
-- 1. Trigger defense — catches tamper regardless of role.
--    Compares OLD vs NEW for every chain-input column. Anything
--    that would invalidate the chain raises an exception.
-- ---------------------------------------------------------------

CREATE OR REPLACE FUNCTION audit_events_block_tamper()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.seq         IS DISTINCT FROM OLD.seq
       OR NEW.actor    IS DISTINCT FROM OLD.actor
       OR NEW.action   IS DISTINCT FROM OLD.action
       OR NEW.resource IS DISTINCT FROM OLD.resource
       OR NEW.metadata::text IS DISTINCT FROM OLD.metadata::text
       OR NEW.prev_hash IS DISTINCT FROM OLD.prev_hash
       OR NEW.occurred_at IS DISTINCT FROM OLD.occurred_at
       OR NEW.created_at  IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION
          'audit_events: chain-input columns are immutable (use the verifier to detect upstream tamper)';
    END IF;
    -- We deliberately permit UPDATE on `hash` only — AppendAudit
    -- writes a placeholder on INSERT and computes the real hash
    -- post-seq-assignment. The role-level GRANT below mirrors
    -- this allow-list.
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_events_tamper_block ON audit_events;
CREATE TRIGGER audit_events_tamper_block
BEFORE UPDATE ON audit_events
FOR EACH ROW EXECUTE FUNCTION audit_events_block_tamper();

CREATE OR REPLACE FUNCTION audit_events_block_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
      'audit_events: rows are append-only (use the verifier to detect upstream tamper)';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_events_delete_block ON audit_events;
CREATE TRIGGER audit_events_delete_block
BEFORE DELETE ON audit_events
FOR EACH ROW EXECUTE FUNCTION audit_events_block_delete();

-- TRUNCATE bypasses BEFORE DELETE triggers; block it explicitly
-- via a TRUNCATE trigger (Postgres ≥ 8.4).
CREATE OR REPLACE FUNCTION audit_events_block_truncate()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
      'audit_events: truncation is forbidden (audit log is append-only)';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_events_truncate_block ON audit_events;
CREATE TRIGGER audit_events_truncate_block
BEFORE TRUNCATE ON audit_events
FOR EACH STATEMENT EXECUTE FUNCTION audit_events_block_truncate();

-- ---------------------------------------------------------------
-- 2. Role-level REVOKE/GRANT — preventive control for the
--    analytics-service writer credential. Mirrors the trigger
--    policy: SELECT + INSERT + UPDATE(hash) only.
-- ---------------------------------------------------------------

REVOKE ALL ON audit_events FROM :"analytics_role";
GRANT SELECT, INSERT ON audit_events TO :"analytics_role";
GRANT UPDATE (hash) ON audit_events TO :"analytics_role";

-- Sequence privileges — BIGSERIAL needs USAGE for nextval()
-- during INSERT. The sequence name follows GORM's convention.
GRANT USAGE, SELECT ON SEQUENCE audit_events_seq_seq TO :"analytics_role";

COMMIT;

-- ---------------------------------------------------------------
-- Post-apply smoke test (manual): every assertion below SHOULD
-- raise. Run as the analytics_role to confirm. Comment out
-- after first verification; left here for reviewability.
--
--   SET ROLE :"analytics_role";
--   UPDATE audit_events SET actor = 'tamper' WHERE seq = 1;
--     -- expected: ERROR: audit_events: chain-input columns are immutable
--   DELETE FROM audit_events WHERE seq = 1;
--     -- expected: ERROR: audit_events: rows are append-only
--   TRUNCATE audit_events;
--     -- expected: ERROR: audit_events: truncation is forbidden
--   RESET ROLE;
-- ---------------------------------------------------------------
