-- Isolation of embeddings/cache/settings between tenants. ROLLBACK at end.
\set ON_ERROR_STOP 1
BEGIN;
SET LOCAL ROLE devbrain_owner;
INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-0000000000a1','emb-a'), ('00000000-0000-0000-0000-0000000000b1','emb-b');
INSERT INTO embedding_indexes (id, name, provider, model, dims) VALUES ('00000000-0000-0000-0000-0000000000c1','test-idx','fake','m',3);
INSERT INTO documents (id, tenant_id, source) VALUES ('00000000-0000-0000-0000-0000000000d1','00000000-0000-0000-0000-0000000000a1','t'), ('00000000-0000-0000-0000-0000000000d2','00000000-0000-0000-0000-0000000000b1','t');
INSERT INTO chunks (id, tenant_id, document_id, ord, content) VALUES
 ('00000000-0000-0000-0000-0000000000e1','00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000d1',0,'a'),
 ('00000000-0000-0000-0000-0000000000e2','00000000-0000-0000-0000-0000000000b1','00000000-0000-0000-0000-0000000000d2',0,'b');
INSERT INTO chunk_embeddings (chunk_id, index_id, tenant_id, embedding) VALUES
 ('00000000-0000-0000-0000-0000000000e1','00000000-0000-0000-0000-0000000000c1','00000000-0000-0000-0000-0000000000a1','[1,0,0]'),
 ('00000000-0000-0000-0000-0000000000e2','00000000-0000-0000-0000-0000000000c1','00000000-0000-0000-0000-0000000000b1','[0,1,0]');
INSERT INTO embedding_cache (tenant_id, content_sha256, provider, model, dims, embedding) VALUES
 ('00000000-0000-0000-0000-0000000000a1','\x01','fake','m',3,'[1,0,0]'), ('00000000-0000-0000-0000-0000000000b1','\x01','fake','m',3,'[0,1,0]');
INSERT INTO tenant_settings VALUES ('00000000-0000-0000-0000-0000000000b1','local_only');
SET LOCAL ROLE devbrain_app;
SELECT set_config('app.tenant_id','00000000-0000-0000-0000-0000000000a1', true);
DO $$
DECLARE n int; ok boolean := false;
BEGIN
  SELECT count(*) INTO n FROM chunk_embeddings; IF n <> 1 THEN RAISE EXCEPTION 'FAIL: A sees % embeddings', n; END IF;
  SELECT count(*) INTO n FROM embedding_cache; IF n <> 1 THEN RAISE EXCEPTION 'FAIL: A sees % cache rows', n; END IF;
  SELECT count(*) INTO n FROM tenant_settings; IF n <> 0 THEN RAISE EXCEPTION 'FAIL: A sees B settings'; END IF;
  SELECT count(*) INTO n FROM embedding_indexes; IF n < 1 THEN RAISE EXCEPTION 'FAIL: registry not readable'; END IF;
  BEGIN
    INSERT INTO embedding_cache (tenant_id, content_sha256, provider, model, dims, embedding)
      VALUES ('00000000-0000-0000-0000-0000000000b1','\x02','fake','m',3,'[0,0,1]');
  EXCEPTION WHEN insufficient_privilege THEN ok := true; END;
  IF NOT ok THEN RAISE EXCEPTION 'FAIL: cross-tenant cache insert allowed'; END IF;
  ok := false;
  BEGIN UPDATE tenant_settings SET provider_policy='external_allowed';
  EXCEPTION WHEN insufficient_privilege THEN ok := true; END;
  IF NOT ok THEN RAISE EXCEPTION 'FAIL: app can modify tenant policy'; END IF;
  RAISE NOTICE 'PASS embeddings/cache/settings isolated';
END $$;
ROLLBACK;
