-- 006: idempotent ingestion by document uri (per tenant).
CREATE UNIQUE INDEX IF NOT EXISTS documents_tenant_uri_uniq ON documents (tenant_id, uri) WHERE uri IS NOT NULL;
CREATE INDEX IF NOT EXISTS documents_source_idx ON documents (tenant_id, source);
