-- 007: reusable recipes learned from request→correction→solution sequences (idempotent, RLS).
CREATE TABLE IF NOT EXISTS recipes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project text NOT NULL DEFAULT '',
  title text NOT NULL,
  problem text NOT NULL,
  solution text NOT NULL,
  code text NOT NULL DEFAULT '',
  versions jsonb NOT NULL DEFAULT '{}',
  files text[] NOT NULL DEFAULT '{}',
  commits text[] NOT NULL DEFAULT '{}',
  corrections int NOT NULL DEFAULT 0,
  occurrences int NOT NULL DEFAULT 1,
  source_conversation_ids text[] NOT NULL DEFAULT '{}',
  source_refs jsonb NOT NULL DEFAULT '[]',
  fingerprint text NOT NULL,
  status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified','verified','rejected')),
  evidence text NOT NULL DEFAULT '',
  approved_by text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT 'learn',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', title || ' ' || problem || ' ' || solution || ' ' || code)) STORED,
  UNIQUE (tenant_id, fingerprint));
CREATE INDEX IF NOT EXISTS recipes_tsv_idx ON recipes USING gin (tsv);
CREATE INDEX IF NOT EXISTS recipes_project_idx ON recipes (tenant_id, project);
ALTER TABLE recipes ENABLE ROW LEVEL SECURITY;
ALTER TABLE recipes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON recipes;
CREATE POLICY tenant_isolation ON recipes
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON recipes TO devbrain_app;
