-- 012: Cotizador IA — catalog + versioned prices, exports (DOCX/PDF) with immutable blobs, adapter config stubs.
CREATE TABLE IF NOT EXISTS service_catalog (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,31}$'),
  name text NOT NULL, description text NOT NULL DEFAULT '', aliases text[] NOT NULL DEFAULT '{}',
  unit text NOT NULL DEFAULT 'und',
  billing_mode text NOT NULL CHECK (billing_mode IN ('one_time','monthly','annual','usage')),
  tax_rule text NOT NULL DEFAULT 'gravado' CHECK (tax_rule IN ('gravado','exonerado','inafecto')),
  active boolean NOT NULL DEFAULT true,
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code));
CREATE TABLE IF NOT EXISTS price_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  service_id uuid NOT NULL REFERENCES service_catalog(id) ON DELETE CASCADE,
  currency text NOT NULL CHECK (currency IN ('PEN','USD')),
  unit_price numeric(18,4) NOT NULL CHECK (unit_price >= 0),
  price_includes_igv boolean NOT NULL DEFAULT false,
  valid_from date NOT NULL DEFAULT current_date, valid_to date,
  source jsonb NOT NULL DEFAULT '{"kind":"manual"}', verified boolean NOT NULL DEFAULT false,
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (valid_to IS NULL OR valid_to >= valid_from));
CREATE INDEX IF NOT EXISTS price_versions_svc_idx ON price_versions (tenant_id, service_id, currency, valid_from DESC);
-- no overlapping validity per service+currency (trigger instead of btree_gist)
CREATE OR REPLACE FUNCTION price_versions_no_overlap() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM price_versions p WHERE p.tenant_id = NEW.tenant_id AND p.service_id = NEW.service_id AND p.currency = NEW.currency AND p.id <> NEW.id
      AND daterange(p.valid_from, COALESCE(p.valid_to, 'infinity'::date), '[]') && daterange(NEW.valid_from, COALESCE(NEW.valid_to, 'infinity'::date), '[]')) THEN
    RAISE EXCEPTION 'price validity overlaps an existing version' USING ERRCODE = '23P01';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS price_versions_no_overlap ON price_versions;
CREATE TRIGGER price_versions_no_overlap BEFORE INSERT OR UPDATE ON price_versions FOR EACH ROW EXECUTE FUNCTION price_versions_no_overlap();

CREATE TABLE IF NOT EXISTS commercial_blobs (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  mime text NOT NULL, size int NOT NULL, data bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, sha256));
CREATE TABLE IF NOT EXISTS quote_exports (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  quote_id uuid NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
  version int NOT NULL, format text NOT NULL CHECK (format IN ('pdf','docx')),
  template_version text NOT NULL, sha256 text NOT NULL, filename text NOT NULL,
  totals_verified boolean NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, sha256) REFERENCES commercial_blobs (tenant_id, sha256));
CREATE INDEX IF NOT EXISTS quote_exports_q_idx ON quote_exports (tenant_id, quote_id, created_at DESC);
-- pluggable adapters (RUC lookup, mail): config stubs only, disabled; secrets never stored in clear here.
CREATE TABLE IF NOT EXISTS integration_providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('ruc_lookup','mail','customer','billing')),
  key text NOT NULL, enabled boolean NOT NULL DEFAULT false,
  config_public jsonb NOT NULL DEFAULT '{}', secret_ref text,
  health_status text NOT NULL DEFAULT 'not_configured', updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, kind, key));
ALTER TABLE commercial_companies ADD COLUMN IF NOT EXISTS default_terms text NOT NULL DEFAULT '';
ALTER TABLE commercial_companies ADD COLUMN IF NOT EXISTS logo_sha256 text;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['service_catalog','price_versions','commercial_blobs','quote_exports','integration_providers'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
      WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, t);
  END LOOP;
END $$;
GRANT SELECT, INSERT, UPDATE ON service_catalog, integration_providers TO devbrain_app;
GRANT SELECT, INSERT, UPDATE ON price_versions TO devbrain_app;           -- update only to close valid_to
GRANT SELECT, INSERT ON commercial_blobs, quote_exports TO devbrain_app;   -- immutable

-- Placeholder terms (editable in Configuración). No invented commercial conditions: generic, clearly marked.
UPDATE commercial_companies SET default_terms = E'[PLANTILLA — editar en Configuración]\n- Precios expresados en la moneda indicada.\n- Validez según la fecha de vigencia de esta cotización.\n- Forma de pago y plazos: por definir.'
  WHERE default_terms = '' AND is_placeholder;

-- Example catalog seed for tenant 'default' (FAKE demo item, verified=false; edit or delete it).
INSERT INTO service_catalog (tenant_id, code, name, description, aliases, unit, billing_mode, tax_rule, created_by)
  SELECT id, 'DEMO-PLAN-BASIC', 'Servicio de ejemplo – Plan Básico',
    'Ítem de demostración con datos ficticios. Reemplázalo por tu propio catálogo.',
    ARRAY['plan basico','servicio de ejemplo','demo basic'], 'mes', 'monthly', 'gravado', 'seed:012'
  FROM tenants WHERE name = 'default' ON CONFLICT DO NOTHING;
INSERT INTO price_versions (tenant_id, service_id, currency, unit_price, price_includes_igv, valid_from, source, verified, created_by)
  SELECT s.tenant_id, s.id, 'PEN', 10, false, DATE '2026-01-01',
    jsonb_build_object('kind','example','note','Precio ficticio de demostración.'),
    false, 'seed:012'
  FROM service_catalog s JOIN tenants t ON t.id = s.tenant_id
  WHERE t.name = 'default' AND s.code = 'DEMO-PLAN-BASIC' AND NOT EXISTS (SELECT 1 FROM price_versions p WHERE p.service_id = s.id);
