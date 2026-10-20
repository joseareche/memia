-- 002: row-level security by tenant_id + grants (idempotent). Run as devbrain_owner.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['users','api_tokens','projects','documents','chunks','memories','audit_log'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
      WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, t);
  END LOOP;
END $$;
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_self ON tenants;
CREATE POLICY tenant_self ON tenants FOR SELECT
  USING (id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- Token lookup happens before the tenant is known: SECURITY DEFINER (owner has BYPASSRLS).
CREATE OR REPLACE FUNCTION auth_lookup_token(p_hash bytea)
RETURNS TABLE (tenant_id uuid, user_id uuid, scopes text[])
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT t.tenant_id, t.user_id, t.scopes FROM api_tokens t
  WHERE t.token_hash = p_hash AND t.revoked_at IS NULL
    AND (t.expires_at IS NULL OR t.expires_at > now())
$$;
REVOKE ALL ON FUNCTION auth_lookup_token(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_token(bytea) TO devbrain_app;

GRANT USAGE ON SCHEMA public TO devbrain_app;
GRANT SELECT ON tenants TO devbrain_app;
GRANT SELECT ON users TO devbrain_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON projects, documents, chunks, memories TO devbrain_app;
GRANT SELECT, INSERT ON audit_log TO devbrain_app;
GRANT USAGE ON SEQUENCE audit_log_id_seq TO devbrain_app;
-- api_tokens: no direct access for the app role (only via auth_lookup_token).
