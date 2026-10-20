-- 011: Cotizador IA — Phase A (local, no external integrations). All tables tenant-scoped with RLS.
-- Money: numeric (never float). Placeholders (company, series) are editable defaults until the user answers.
CREATE TABLE IF NOT EXISTS commercial_settings (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  default_currency text NOT NULL DEFAULT 'PEN' CHECK (default_currency IN ('PEN','USD')),
  igv_rate numeric(7,4) NOT NULL DEFAULT 0.18 CHECK (igv_rate >= 0 AND igv_rate < 1),
  prices_include_igv boolean NOT NULL DEFAULT false,
  default_validity_days int NOT NULL DEFAULT 15 CHECK (default_validity_days BETWEEN 1 AND 365),
  is_placeholder boolean NOT NULL DEFAULT true,
  updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS commercial_companies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  legal_name text NOT NULL, trade_name text NOT NULL DEFAULT '', tax_id text NOT NULL DEFAULT '',
  address text NOT NULL DEFAULT '', email text NOT NULL DEFAULT '', phone text NOT NULL DEFAULT '',
  website text NOT NULL DEFAULT '', footer text NOT NULL DEFAULT '',
  is_placeholder boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS commercial_series (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  company_id uuid NOT NULL REFERENCES commercial_companies(id) ON DELETE CASCADE,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9]{1,8}$'),
  pattern text NOT NULL DEFAULT '{CODE}-{YYYY}-{N5}',
  reset_yearly boolean NOT NULL DEFAULT true,
  next_number bigint NOT NULL DEFAULT 1 CHECK (next_number >= 1),
  current_year int NOT NULL DEFAULT 0,
  is_default boolean NOT NULL DEFAULT true,
  UNIQUE (tenant_id, company_id, code));
CREATE TABLE IF NOT EXISTS commercial_customers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  display_name text NOT NULL, legal_name text NOT NULL DEFAULT '',
  document_type text NOT NULL DEFAULT '' CHECK (document_type IN ('','RUC','DNI','CE','PAS','OTRO')),
  tax_id text NOT NULL DEFAULT '', tax_id_norm text,
  email text NOT NULL DEFAULT '', phone text NOT NULL DEFAULT '', contact_name text NOT NULL DEFAULT '',
  address text NOT NULL DEFAULT '', notes text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS commercial_customers_taxid_uq ON commercial_customers (tenant_id, document_type, tax_id_norm) WHERE tax_id_norm IS NOT NULL;
CREATE INDEX IF NOT EXISTS commercial_customers_name_idx ON commercial_customers (tenant_id, lower(display_name));
CREATE TABLE IF NOT EXISTS commercial_members (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email text NOT NULL CHECK (email = lower(email)),
  role text NOT NULL CHECK (role IN ('viewer','seller','approver','admin')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, email));
CREATE TABLE IF NOT EXISTS quotes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  company_id uuid NOT NULL REFERENCES commercial_companies(id),
  series_id uuid NOT NULL REFERENCES commercial_series(id),
  number text,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','pending_approval','approved','sent','accepted','rejected','expired','cancelled')),
  title text NOT NULL DEFAULT '',
  customer_kind text NOT NULL CHECK (customer_kind IN ('local','free')),
  customer_id uuid REFERENCES commercial_customers(id),
  customer_snapshot jsonb NOT NULL,
  currency text NOT NULL CHECK (currency IN ('PEN','USD')),
  prices_include_igv boolean NOT NULL,
  igv_rate numeric(7,4) NOT NULL CHECK (igv_rate >= 0 AND igv_rate < 1),
  global_discount_pct numeric(7,4) NOT NULL DEFAULT 0 CHECK (global_discount_pct >= 0 AND global_discount_pct <= 100),
  valid_until date,
  terms text NOT NULL DEFAULT '',
  totals jsonb NOT NULL DEFAULT '{}',
  current_version int NOT NULL DEFAULT 1,
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  CHECK ((customer_kind = 'local') = (customer_id IS NOT NULL)));
CREATE UNIQUE INDEX IF NOT EXISTS quotes_number_uq ON quotes (tenant_id, number) WHERE number IS NOT NULL;
CREATE INDEX IF NOT EXISTS quotes_list_idx ON quotes (tenant_id, status, updated_at DESC);
CREATE TABLE IF NOT EXISTS quote_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  quote_id uuid NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
  ord int NOT NULL,
  description text NOT NULL, unit text NOT NULL DEFAULT 'und',
  qty numeric(18,4) NOT NULL CHECK (qty > 0),
  unit_price numeric(18,4) NOT NULL CHECK (unit_price >= 0),
  unit_cost numeric(18,4),
  discount_pct numeric(7,4) NOT NULL DEFAULT 0 CHECK (discount_pct >= 0 AND discount_pct <= 100),
  discount_amount numeric(18,4) NOT NULL DEFAULT 0 CHECK (discount_amount >= 0),
  billing_mode text NOT NULL CHECK (billing_mode IN ('one_time','monthly','annual','usage')),
  tax_rule text NOT NULL CHECK (tax_rule IN ('gravado','exonerado','inafecto')),
  price_source jsonb NOT NULL DEFAULT '{"kind":"manual"}',
  UNIQUE (quote_id, ord));
CREATE TABLE IF NOT EXISTS quote_versions (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  quote_id uuid NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
  version int NOT NULL,
  status text NOT NULL,
  snapshot jsonb NOT NULL,
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (quote_id, version));
CREATE TABLE IF NOT EXISTS quote_events (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  quote_id uuid NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
  actor text NOT NULL, action text NOT NULL,
  from_status text, to_status text,
  detail jsonb NOT NULL DEFAULT '{}',
  at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS quote_events_q_idx ON quote_events (tenant_id, quote_id, id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['commercial_settings','commercial_companies','commercial_series','commercial_customers','commercial_members','quotes','quote_items','quote_versions','quote_events'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
      WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, t);
  END LOOP;
END $$;
GRANT SELECT, INSERT, UPDATE ON commercial_settings, commercial_companies, commercial_series, commercial_customers, quotes TO devbrain_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON quote_items TO devbrain_app;
GRANT SELECT ON commercial_members TO devbrain_app;
-- append-only / immutable for the app role
GRANT SELECT, INSERT ON quote_versions, quote_events TO devbrain_app;
GRANT USAGE ON SEQUENCE quote_events_id_seq TO devbrain_app;

-- Seed placeholders for tenant 'default' (clearly marked; editable via API).
INSERT INTO commercial_settings (tenant_id) SELECT id FROM tenants WHERE name = 'default' ON CONFLICT DO NOTHING;
INSERT INTO commercial_companies (tenant_id, legal_name, trade_name, footer)
  SELECT t.id, 'EMPRESA EMISORA (configurar)', 'Memia', 'Datos de empresa pendientes de configurar.' FROM tenants t
  WHERE t.name = 'default' AND NOT EXISTS (SELECT 1 FROM commercial_companies c WHERE c.tenant_id = t.id);
INSERT INTO commercial_series (tenant_id, company_id, code)
  SELECT c.tenant_id, c.id, 'COT' FROM commercial_companies c JOIN tenants t ON t.id = c.tenant_id WHERE t.name = 'default'
  ON CONFLICT DO NOTHING;
INSERT INTO commercial_members (tenant_id, email, role) SELECT id, 'admin@example.com', 'admin' FROM tenants WHERE name = 'default' ON CONFLICT DO NOTHING;
-- the chat service token may call /v1/commercial/* (the end-user role still comes from the signed actor)
UPDATE api_tokens SET scopes = array_append(scopes, 'commercial') WHERE name = 'web-chat' AND NOT ('commercial' = ANY (scopes));
