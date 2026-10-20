-- Platform users + module entitlements (owner decides per user which modules are enabled).
-- Platform-level (not tenant data): no RLS; the app role may read, and the VPN-only admin API writes.
CREATE TABLE IF NOT EXISTS platform_modules (
  key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{1,30}$'),
  name text NOT NULL, description text NOT NULL DEFAULT '',
  roles text[] NOT NULL DEFAULT '{}', default_role text, sort int NOT NULL DEFAULT 100,
  CHECK (default_role IS NULL OR default_role = ANY (roles)));
INSERT INTO platform_modules (key, name, description, roles, default_role, sort) VALUES
  ('chat', 'Chat IA', 'Asistente y conversaciones en chat.example.com/chat', '{}', NULL, 10),
  ('notes', 'Notas / base de conocimiento', 'Buscar, guardar y preguntar sobre las notas del tenant asignado', '{}', NULL, 20),
  ('cotizador', 'Cotizador', 'Cotizaciones, catálogo, aprobación y PDF', '{viewer,seller,approver,admin}', 'viewer', 30)
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS platform_users (
  email text PRIMARY KEY CHECK (email = lower(email) AND email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
  display_name text NOT NULL DEFAULT '',
  tenant_id uuid REFERENCES tenants(id),           -- NULL = no knowledge base / no tenant data
  enabled boolean NOT NULL DEFAULT true,
  superadmin boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT 'migration', created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now());

CREATE TABLE IF NOT EXISTS user_modules (
  email text NOT NULL REFERENCES platform_users(email) ON DELETE CASCADE,
  module text NOT NULL REFERENCES platform_modules(key),
  enabled boolean NOT NULL DEFAULT true,
  role text,
  updated_by text NOT NULL DEFAULT 'migration', updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (email, module));

CREATE TABLE IF NOT EXISTS entitlement_audit (
  id bigserial PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(),
  actor text NOT NULL, source_ip text, action text NOT NULL,
  email text NOT NULL, module text, before jsonb, after jsonb);
CREATE INDEX IF NOT EXISTS entitlement_audit_email_idx ON entitlement_audit (email, id DESC);

GRANT SELECT ON platform_modules TO devbrain_app;
GRANT SELECT, INSERT, UPDATE ON platform_users, user_modules TO devbrain_app;   -- no DELETE: disable instead
GRANT SELECT, INSERT ON entitlement_audit TO devbrain_app;                     -- append-only
GRANT USAGE ON SEQUENCE entitlement_audit_id_seq TO devbrain_app;

-- Owner = superadmin with every module.
INSERT INTO platform_users (email, display_name, tenant_id, superadmin)
  SELECT 'admin@example.com', 'Memia', id, true FROM tenants WHERE name = 'default'
  ON CONFLICT (email) DO UPDATE SET superadmin = true, enabled = true;
INSERT INTO user_modules (email, module, enabled, role)
  SELECT 'admin@example.com', key, true, CASE WHEN key = 'cotizador' THEN 'admin' END FROM platform_modules
  ON CONFLICT DO NOTHING;
-- Existing commercial members keep their access (cotizador role carried over).
INSERT INTO platform_users (email, tenant_id)
  SELECT DISTINCT ON (email) email, tenant_id FROM commercial_members ORDER BY email, tenant_id ON CONFLICT DO NOTHING;
INSERT INTO user_modules (email, module, enabled, role)
  SELECT DISTINCT ON (m.email) m.email, 'cotizador', true, m.role FROM commercial_members m
  JOIN platform_users u ON u.email = m.email AND u.tenant_id = m.tenant_id ORDER BY m.email
  ON CONFLICT DO NOTHING;
INSERT INTO entitlement_audit (actor, action, email, after)
  SELECT 'migration-013', 'seed', email, jsonb_build_object('superadmin', superadmin) FROM platform_users;

-- Tenant names for the admin user editor (tenants is RLS-protected; this exposes only id+name).
CREATE OR REPLACE FUNCTION platform_tenants() RETURNS TABLE (id uuid, name text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$ SELECT id, name FROM tenants ORDER BY name $$;
REVOKE ALL ON FUNCTION platform_tenants() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_tenants() TO devbrain_app;
