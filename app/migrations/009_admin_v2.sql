-- 009: sync run log (no content) + admin_stats_v2(period, tenant, project) for the dashboard v2.
CREATE TABLE IF NOT EXISTS sync_runs (
  id bigserial PRIMARY KEY,
  run text NOT NULL UNIQUE,
  source text NOT NULL DEFAULT 'cursor',
  finished_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL,
  summary jsonb NOT NULL DEFAULT '{}');
REVOKE ALL ON sync_runs FROM PUBLIC;

CREATE OR REPLACE FUNCTION admin_stats_v2(p_period text DEFAULT '30d', p_tenant text DEFAULT '', p_project text DEFAULT '') RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
WITH day0 AS (SELECT (date_trunc('day', now() AT TIME ZONE 'America/Lima') AT TIME ZONE 'America/Lima') AS t),
since AS (SELECT CASE p_period WHEN 'today' THEN (SELECT t FROM day0) WHEN '7d' THEN now()-interval '7 days' ELSE now()-interval '30 days' END AS t),
tn AS (SELECT id, name FROM tenants WHERE p_tenant = '' OR name = p_tenant),
e30 AS (SELECT u.*, t.name AS tenant, (u.operation IN ('embed_ingest','distill_map','distill_reduce')) AS is_import
        FROM usage_events u JOIN tn t ON t.id=u.tenant_id WHERE u.created_at > now() - interval '30 days'),
e AS (SELECT * FROM e30 WHERE created_at >= (SELECT t FROM since)),
docs AS (SELECT d.* FROM documents d JOIN tn ON tn.id=d.tenant_id LEFT JOIN projects p ON p.id=d.project_id
         WHERE p_project = '' OR p.name = p_project)
SELECT jsonb_build_object(
 'generated_at', now(), 'period', p_period, 'tenant', p_tenant, 'project', p_project,
 'filters', jsonb_build_object('tenants', (SELECT COALESCE(jsonb_agg(name ORDER BY name),'[]') FROM tenants),
    'projects', (SELECT COALESCE(jsonb_agg(jsonb_build_object('tenant', t.name, 'name', p.name) ORDER BY p.name),'[]') FROM projects p JOIN tn t ON t.id=p.tenant_id)),
 'kpi', (SELECT jsonb_build_object(
    'cost_today', (SELECT COALESCE(sum(cost_usd),0) FROM e30 WHERE created_at >= (SELECT t FROM day0)),
    'cost_30d', COALESCE((SELECT sum(cost_usd) FROM e30),0),
    'import_30d', COALESCE((SELECT sum(cost_usd) FROM e30 WHERE is_import),0),
    'usage_30d', COALESCE((SELECT sum(cost_usd) FROM e30 WHERE NOT is_import),0),
    'cost', COALESCE(sum(cost_usd),0), 'import_cost', COALESCE(sum(cost_usd) FILTER (WHERE is_import),0),
    'usage_cost', COALESCE(sum(cost_usd) FILTER (WHERE NOT is_import),0),
    'requests', count(*), 'input_tokens', COALESCE(sum(input_tokens),0), 'output_tokens', COALESCE(sum(output_tokens),0),
    'saved_usd', COALESCE(sum(saved_usd),0), 'cache_hits', COALESCE(sum(cache_hits),0), 'cache_misses', COALESCE(sum(cache_misses),0),
    'errors', count(*) FILTER (WHERE status='error'), 'policy_refused', count(*) FILTER (WHERE status='policy_refused'),
    'redacted_items', COALESCE(sum((SELECT COALESCE(sum(v::int),0) FROM jsonb_each_text(redactions) r(k,v))),0)) FROM e),
 'per_day', COALESCE((SELECT jsonb_agg(d ORDER BY d->>'day') FROM (SELECT jsonb_build_object('day', to_char(created_at AT TIME ZONE 'America/Lima','YYYY-MM-DD'),
    'requests', count(*), 'import_usd', COALESCE(sum(cost_usd) FILTER (WHERE is_import),0), 'usage_usd', COALESCE(sum(cost_usd) FILTER (WHERE NOT is_import),0),
    'saved_usd', sum(saved_usd)) d FROM e30 GROUP BY to_char(created_at AT TIME ZONE 'America/Lima','YYYY-MM-DD')) x),'[]'),
 'per_tenant', COALESCE((SELECT jsonb_agg(jsonb_build_object('tenant', tenant, 'requests', n, 'cost_usd', c, 'saved_usd', s, 'tokens', t) ORDER BY c DESC)
    FROM (SELECT tenant, count(*) n, sum(cost_usd) c, sum(saved_usd) s, sum(input_tokens+output_tokens) t FROM e GROUP BY tenant) x),'[]'),
 'per_operation', COALESCE((SELECT jsonb_agg(jsonb_build_object('operation', operation, 'model', provider||'/'||model, 'requests', n,
    'input_tokens', it, 'output_tokens', ot, 'cost_usd', c, 'saved_usd', s, 'cache_hits', h, 'cache_misses', m, 'errors', er, 'p50_ms', p50, 'p95_ms', p95) ORDER BY c DESC)
    FROM (SELECT operation, provider, model, count(*) n, sum(input_tokens) it, sum(output_tokens) ot, sum(cost_usd) c, sum(saved_usd) s,
      sum(cache_hits) h, sum(cache_misses) m, count(*) FILTER (WHERE status<>'ok') er,
      round((percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='ok'))::numeric) p50,
      round((percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='ok'))::numeric) p95
      FROM e GROUP BY operation, provider, model) x),'[]'),
 'redactions_by_type', COALESCE((SELECT jsonb_object_agg(k, n) FROM (SELECT k, sum(v::int) n FROM e, jsonb_each_text(e.redactions) r(k,v) GROUP BY k) x),'{}'),
 'errors_by_kind', COALESCE((SELECT jsonb_object_agg(k, n) FROM (SELECT operation||':'||COALESCE(NULLIF(error_kind,''),status) k, count(*) n FROM e WHERE status<>'ok' GROUP BY 1) x),'{}'),
 'recent_errors', COALESCE((SELECT jsonb_agg(r) FROM (SELECT jsonb_build_object('at', created_at, 'tenant', tenant, 'operation', operation, 'status', status, 'error_kind', error_kind, 'latency_ms', latency_ms) r
    FROM e WHERE status<>'ok' ORDER BY created_at DESC LIMIT 10) x),'[]'),
 'content', jsonb_build_object('documents', (SELECT count(*) FROM docs), 'chunks', (SELECT count(*) FROM chunks c JOIN docs d ON d.id=c.document_id),
    'projects', (SELECT count(*) FROM projects p JOIN tn ON tn.id=p.tenant_id WHERE p_project='' OR p.name=p_project),
    'knowledge_docs', (SELECT count(*) FROM docs WHERE source='knowledge'),
    'embeddings', (SELECT count(*) FROM chunk_embeddings ce JOIN chunks c ON c.id=ce.chunk_id JOIN docs d ON d.id=c.document_id),
    'cache_entries', (SELECT count(*) FROM embedding_cache ec JOIN tn ON tn.id=ec.tenant_id),
    'recipes', (SELECT count(*) FROM recipes r JOIN tn ON tn.id=r.tenant_id WHERE p_project='' OR r.project=p_project)),
 'pending', (SELECT jsonb_build_object('open', count(*) FILTER (WHERE status='open'), 'done', count(*) FILTER (WHERE status='done'),
    'latest', max(source_date)) FROM pending_items pi JOIN tn ON tn.id=pi.tenant_id WHERE p_project='' OR pi.project=p_project),
 'last_sync', (SELECT jsonb_build_object('run', run, 'finished_at', finished_at, 'status', status, 'summary', summary) FROM sync_runs ORDER BY finished_at DESC LIMIT 1),
 'syncs_30d', (SELECT jsonb_build_object('runs', count(*), 'ok', count(*) FILTER (WHERE status='ok'), 'skipped', count(*) FILTER (WHERE status='skipped')) FROM sync_runs WHERE finished_at > now()-interval '30 days'),
 'active_index', (SELECT jsonb_build_object('name', i.name, 'provider', i.provider, 'model', i.model, 'dims', i.dims, 'activated_at', i.activated_at,
    'embeddings', (SELECT count(*) FROM chunk_embeddings ce WHERE ce.index_id=i.id),
    'chunks_missing', (SELECT count(*) FROM chunks c WHERE NOT EXISTS (SELECT 1 FROM chunk_embeddings ce WHERE ce.chunk_id=c.id AND ce.index_id=i.id))) FROM embedding_indexes i WHERE i.status='active'),
 'recent', COALESCE((SELECT jsonb_agg(r) FROM (SELECT jsonb_build_object('at', created_at, 'tenant', tenant, 'operation', operation, 'model', provider||'/'||model,
    'status', status, 'error_kind', error_kind, 'items', items, 'cache_hits', cache_hits, 'cache_misses', cache_misses,
    'tokens', input_tokens+output_tokens, 'latency_ms', latency_ms, 'cost_usd', cost_usd) r FROM e ORDER BY created_at DESC LIMIT 25) x),'[]'))
$$;
REVOKE ALL ON FUNCTION admin_stats_v2(text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_stats_v2(text,text,text) TO devbrain_app;
