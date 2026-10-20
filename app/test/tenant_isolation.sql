-- Tenant-isolation test. Runs inside a transaction and ROLLBACKs (leaves no data).
\set ON_ERROR_STOP 1
BEGIN;
SET LOCAL ROLE devbrain_owner;
INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-00000000000a','test-a'), ('00000000-0000-0000-0000-00000000000b','test-b');
INSERT INTO documents (tenant_id, source, title) VALUES
  ('00000000-0000-0000-0000-00000000000a','test','doc-a'),
  ('00000000-0000-0000-0000-00000000000b','test','doc-b');
SET LOCAL ROLE devbrain_app;
SELECT set_config('app.tenant_id','00000000-0000-0000-0000-00000000000a', true);
DO $$
DECLARE n int; ok boolean := false;
BEGIN
  SELECT count(*) INTO n FROM documents; IF n <> 1 THEN RAISE EXCEPTION 'FAIL: tenant A sees % docs', n; END IF;
  SELECT count(*) INTO n FROM documents WHERE title = 'doc-b'; IF n <> 0 THEN RAISE EXCEPTION 'FAIL: A sees B'; END IF;
  SELECT count(*) INTO n FROM tenants; IF n <> 1 THEN RAISE EXCEPTION 'FAIL: A sees % tenants', n; END IF;
  BEGIN
    INSERT INTO documents (tenant_id, source) VALUES ('00000000-0000-0000-0000-00000000000b','x');
  EXCEPTION WHEN insufficient_privilege THEN ok := true; END;
  IF NOT ok THEN RAISE EXCEPTION 'FAIL: cross-tenant insert allowed'; END IF;
  BEGIN
    PERFORM 1 FROM api_tokens; RAISE EXCEPTION 'FAIL: app can read api_tokens';
  EXCEPTION WHEN insufficient_privilege THEN NULL; END;
  RAISE NOTICE 'PASS tenant A isolated';
END $$;
SELECT set_config('app.tenant_id','', true);
DO $$ DECLARE n int; BEGIN SELECT count(*) INTO n FROM documents;
  IF n <> 0 THEN RAISE EXCEPTION 'FAIL: no-tenant sees % docs', n; END IF; RAISE NOTICE 'PASS no tenant sees nothing'; END $$;
ROLLBACK;
