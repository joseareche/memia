-- 010: chat "Guardar nota" documents are personal notes: source=note, metadata.kind/created_at (idempotent).
UPDATE documents SET source = 'note',
  metadata = metadata || jsonb_build_object('kind', 'note', 'created_at', to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
WHERE source = 'chat';
CREATE INDEX IF NOT EXISTS documents_notes_recent_idx ON documents (tenant_id, created_at DESC) WHERE source = 'note';
