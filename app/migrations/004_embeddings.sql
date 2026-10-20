-- 004: index registry, per-index embeddings, tenant-scoped cache, tenant provider policy (idempotent).
CREATE TABLE IF NOT EXISTS embedding_indexes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  provider text NOT NULL,
  model text NOT NULL,
  model_version text NOT NULL DEFAULT '',
  dims int NOT NULL CHECK (dims > 0 AND dims <= 16000),
  status text NOT NULL DEFAULT 'building' CHECK (status IN ('building','active','retired')),
  created_at timestamptz NOT NULL DEFAULT now(),
  activated_at timestamptz,
  retired_at timestamptz);
CREATE UNIQUE INDEX IF NOT EXISTS embedding_indexes_one_active ON embedding_indexes ((status)) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS chunk_embeddings (
  chunk_id uuid NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
  index_id uuid NOT NULL REFERENCES embedding_indexes(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  embedding vector NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (chunk_id, index_id));
CREATE INDEX IF NOT EXISTS chunk_embeddings_tenant_idx ON chunk_embeddings (tenant_id, index_id);

-- Key: sha256(text actually sent, i.e. post-redaction) + provider + model + dims, scoped per tenant.
CREATE TABLE IF NOT EXISTS embedding_cache (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  content_sha256 bytea NOT NULL,
  provider text NOT NULL,
  model text NOT NULL,
  dims int NOT NULL,
  embedding vector NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, content_sha256, provider, model, dims));

CREATE TABLE IF NOT EXISTS tenant_settings (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  provider_policy text NOT NULL DEFAULT 'external_allowed' CHECK (provider_policy IN ('external_allowed','local_only')),
  updated_at timestamptz NOT NULL DEFAULT now());

-- Superseded by chunk_embeddings (no data existed).
ALTER TABLE chunks DROP COLUMN IF EXISTS embedding;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['chunk_embeddings','embedding_cache','tenant_settings'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
      WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, t);
  END LOOP;
END $$;

GRANT SELECT ON embedding_indexes TO devbrain_app;
GRANT SELECT, INSERT, DELETE ON chunk_embeddings TO devbrain_app;
GRANT SELECT, INSERT ON embedding_cache TO devbrain_app;
GRANT SELECT ON tenant_settings TO devbrain_app;
