-- 008: open pending items / TODOs extracted from conversations (deduped per tenant, RLS).
CREATE TABLE IF NOT EXISTS pending_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project text NOT NULL DEFAULT '',
  text text NOT NULL,
  fingerprint text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','done')),
  source text NOT NULL DEFAULT 'cursor',
  source_conversation_id text NOT NULL DEFAULT '',
  source_title text NOT NULL DEFAULT '',
  source_date timestamptz,
  source_document_id uuid REFERENCES documents(id) ON DELETE SET NULL,
  occurrences int NOT NULL DEFAULT 1,
  extracted_by text NOT NULL DEFAULT 'llm',
  done_reason text NOT NULL DEFAULT '',
  done_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, fingerprint));
CREATE INDEX IF NOT EXISTS pending_items_open_idx ON pending_items (tenant_id, status, project, source_date DESC);
CREATE INDEX IF NOT EXISTS pending_items_conv_idx ON pending_items (tenant_id, source_conversation_id);
ALTER TABLE pending_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE pending_items FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON pending_items;
CREATE POLICY tenant_isolation ON pending_items
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON pending_items TO devbrain_app;
