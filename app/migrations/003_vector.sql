-- 003: embeddings if pgvector is installed; otherwise full-text only (documented fallback).
-- Dimension 1536 is a placeholder. TODO(embeddings): confirm once the provider is chosen.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
    EXECUTE 'ALTER TABLE chunks ADD COLUMN IF NOT EXISTS embedding vector(1536)';
    EXECUTE 'ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedding vector(1536)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_cosine_ops)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS memories_embedding_hnsw ON memories USING hnsw (embedding vector_cosine_ops)';
  ELSE
    RAISE NOTICE 'pgvector not installed: full-text search only (chunks.tsv)';
  END IF;
END $$;
