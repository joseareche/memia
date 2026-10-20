-- 005: usage_events (provider calls, cache, cost estimates, refusals) + admin_stats() (idempotent).
CREATE TABLE IF NOT EXISTS usage_events (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  operation text NOT NULL,             -- embed_ingest | embed_search | answer
  provider text NOT NULL DEFAULT '',
  model text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','error','policy_refused')),
  error_kind text NOT NULL DEFAULT '',
  items int NOT NULL DEFAULT 0,
  cache_hits int NOT NULL DEFAULT 0,
  cache_misses int NOT NULL DEFAULT 0,
  input_tokens int NOT NULL DEFAULT 0,
  output_tokens int NOT NULL DEFAULT 0,
  saved_tokens_est int NOT NULL DEFAULT 0,
  latency_ms int NOT NULL DEFAULT 0,
  cost_usd numeric(14,8) NOT NULL DEFAULT 0,
  saved_usd numeric(14,8) NOT NULL DEFAULT 0,
  redactions jsonb NOT NULL DEFAULT '{}');   -- counts by type only, never values
CREATE INDEX IF NOT EXISTS usage_events_time_idx ON usage_events (created_at DESC);
CREATE INDEX IF NOT EXISTS usage_events_tenant_idx ON usage_events (tenant_id, created_at DESC);
ALTER TABLE usage_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON usage_events;
CREATE POLICY tenant_isolation ON usage_events
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT ON usage_events TO devbrain_app;
GRANT USAGE ON SEQUENCE usage_events_id_seq TO devbrain_app;

-- Cross-tenant aggregates for the admin view. SECURITY DEFINER (owner, BYPASSRLS); returns no content.
CREATE OR REPLACE FUNCTION admin_stats() RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
WITH day0 AS (SELECT (date_trunc('day', now() AT TIME ZONE 'America/Lima') AT TIME ZONE 'America/Lima') AS t),
e AS (SELECT u.*, tn.name AS tenant FROM usage_events u JOIN tenants tn ON tn.id=u.tenant_id WHERE u.created_at > now() - interval '30 days'),
win AS (SELECT w.name, w.since FROM (VALUES ('today', (SELECT t FROM day0)), ('7d', now()-interval '7 days'), ('30d', now()-interval '30 days')) w(name, since))
SELECT jsonb_build_object(
 'generated_at', now(),
 'totals', (SELECT jsonb_object_agg(w.name, (SELECT jsonb_build_object(
    'requests', count(*), 'input_tokens', COALESCE(sum(input_tokens),0), 'output_tokens', COALESCE(sum(output_tokens),0),
    'cost_usd', COALESCE(sum(cost_usd),0), 'saved_usd', COALESCE(sum(saved_usd),0),
    'cache_hits', COALESCE(sum(cache_hits),0), 'cache_misses', COALESCE(sum(cache_misses),0),
    'errors', count(*) FILTER (WHERE status='error'), 'policy_refused', count(*) FILTER (WHERE status='policy_refused'),
    'redacted_items', COALESCE(sum((SELECT COALESCE(sum(v::int),0) FROM jsonb_each_text(redactions) r(k,v))),0))
    FROM e WHERE e.created_at >= w.since)) FROM win w),
 'per_day', COALESCE((SELECT jsonb_agg(d ORDER BY d->>'day') FROM (SELECT jsonb_build_object('day', to_char(created_at AT TIME ZONE 'America/Lima','YYYY-MM-DD'),
    'requests', count(*), 'cost_usd', sum(cost_usd), 'saved_usd', sum(saved_usd), 'tokens', sum(input_tokens+output_tokens)) d
    FROM e GROUP BY to_char(created_at AT TIME ZONE 'America/Lima','YYYY-MM-DD')) x),'[]'),
 'per_tenant', COALESCE((SELECT jsonb_agg(jsonb_build_object('tenant', tenant, 'requests', n, 'cost_usd', c, 'saved_usd', s, 'tokens', t) ORDER BY c DESC)
    FROM (SELECT tenant, count(*) n, sum(cost_usd) c, sum(saved_usd) s, sum(input_tokens+output_tokens) t FROM e GROUP BY tenant) x),'[]'),
 'per_operation', COALESCE((SELECT jsonb_agg(jsonb_build_object('operation', operation, 'provider', provider, 'model', model, 'requests', n,
    'input_tokens', it, 'output_tokens', ot, 'cost_usd', c, 'saved_usd', s, 'cache_hits', h, 'cache_misses', m, 'p50_ms', p50, 'p95_ms', p95) ORDER BY operation)
    FROM (SELECT operation, provider, model, count(*) n, sum(input_tokens) it, sum(output_tokens) ot, sum(cost_usd) c, sum(saved_usd) s,
      sum(cache_hits) h, sum(cache_misses) m,
      round((percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='ok'))::numeric) p50,
      round((percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='ok'))::numeric) p95
      FROM e GROUP BY operation, provider, model) x),'[]'),
 'redactions_by_type', COALESCE((SELECT jsonb_object_agg(k, n) FROM (SELECT k, sum(v::int) n FROM e, jsonb_each_text(e.redactions) r(k,v) GROUP BY k) x),'{}'),
 'errors_by_kind', COALESCE((SELECT jsonb_object_agg(k, n) FROM (SELECT operation||':'||COALESCE(NULLIF(error_kind,''),status) k, count(*) n FROM e WHERE status<>'ok' GROUP BY 1) x),'{}'),
 'content', COALESCE((SELECT jsonb_agg(jsonb_build_object('tenant', tn.name,
    'documents', (SELECT count(*) FROM documents d WHERE d.tenant_id=tn.id),
    'chunks', (SELECT count(*) FROM chunks c WHERE c.tenant_id=tn.id),
    'embeddings', (SELECT count(*) FROM chunk_embeddings ce WHERE ce.tenant_id=tn.id),
    'cache_entries', (SELECT count(*) FROM embedding_cache ec WHERE ec.tenant_id=tn.id),
    'policy', COALESCE((SELECT provider_policy FROM tenant_settings s WHERE s.tenant_id=tn.id),'external_allowed')) ORDER BY tn.name) FROM tenants tn),'[]'),
 'active_index', (SELECT jsonb_build_object('name', i.name, 'provider', i.provider, 'model', i.model, 'dims', i.dims, 'activated_at', i.activated_at,
    'embeddings', (SELECT count(*) FROM chunk_embeddings ce WHERE ce.index_id=i.id)) FROM embedding_indexes i WHERE i.status='active'),
 'recent', COALESCE((SELECT jsonb_agg(r) FROM (SELECT jsonb_build_object('at', created_at, 'tenant', tenant, 'operation', operation, 'provider', provider,
    'model', model, 'status', status, 'error_kind', error_kind, 'items', items, 'cache_hits', cache_hits, 'cache_misses', cache_misses,
    'input_tokens', input_tokens, 'output_tokens', output_tokens, 'latency_ms', latency_ms, 'cost_usd', cost_usd, 'saved_usd', saved_usd) r
    FROM e ORDER BY created_at DESC LIMIT 30) x),'[]'))
$$;
REVOKE ALL ON FUNCTION admin_stats() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_stats() TO devbrain_app;
