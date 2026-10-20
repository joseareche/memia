// Package embedsvc embeds texts for an index: policy + redaction (provider.Prepare),
// then tenant-scoped cache lookup keyed by sha256(sent text)+provider+model+dims,
// then provider call for misses only.
package embedsvc

import (
	"context"
	"crypto/sha256"

	"devbrain/internal/provider"
	"devbrain/internal/redact"
	"devbrain/internal/store"
)

type Result struct {
	Vectors    [][]float32
	Redactions redact.Stats
	CacheHits  int
	Computed   int
	// HitTokensEst: rough tokens avoided by cache hits (len/4); used only for savings estimates.
	HitTokensEst int
}

type Service struct{ Router *provider.Router }

const batch = 64

func (s *Service) Embed(ctx context.Context, q store.DBTX, tenantID string, pol provider.Policy, idx *store.Index, texts []string) (*Result, error) {
	e, err := s.Router.Get(idx.Provider)
	if err != nil {
		return nil, err
	}
	sent, st, err := provider.Prepare(pol, e.External(), texts)
	if err != nil {
		return nil, err
	}
	res := &Result{Vectors: make([][]float32, len(texts)), Redactions: st}
	hashes := make([][]byte, len(sent))
	pos := map[string][]int{}
	for i, t := range sent {
		h := sha256.Sum256([]byte(t))
		hashes[i] = h[:]
		pos[string(h[:])] = append(pos[string(h[:])], i)
	}
	rows, err := q.Query(ctx, `SELECT content_sha256, embedding::text FROM embedding_cache
		WHERE tenant_id=$1::uuid AND provider=$2 AND model=$3 AND dims=$4 AND content_sha256 = ANY($5)`,
		tenantID, idx.Provider, idx.Model, idx.Dims, hashes)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h []byte
		var v string
		if err := rows.Scan(&h, &v); err != nil {
			rows.Close()
			return nil, err
		}
		vec, err := store.ParseVec(v)
		if err != nil {
			rows.Close()
			return nil, err
		}
		for _, i := range pos[string(h)] {
			res.Vectors[i] = vec
			res.CacheHits++
			res.HitTokensEst += (len(sent[i]) + 3) / 4
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var miss []int
	for i := range sent {
		if res.Vectors[i] == nil {
			miss = append(miss, i)
		}
	}
	for start := 0; start < len(miss); start += batch {
		end := min(start+batch, len(miss))
		in := make([]string, 0, end-start)
		for _, i := range miss[start:end] {
			in = append(in, sent[i])
		}
		vecs, err := e.Embed(ctx, idx.Model, idx.Dims, in)
		if err != nil {
			return nil, err
		}
		for k, i := range miss[start:end] {
			res.Vectors[i] = vecs[k]
			res.Computed++
			if _, err := q.Exec(ctx, `INSERT INTO embedding_cache (tenant_id, content_sha256, provider, model, dims, embedding)
				VALUES ($1::uuid,$2,$3,$4,$5,$6::vector) ON CONFLICT DO NOTHING`,
				tenantID, hashes[i], idx.Provider, idx.Model, idx.Dims, store.VecLiteral(vecs[k])); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

// StoreChunkEmbeddings writes vectors for chunk ids under an index.
func StoreChunkEmbeddings(ctx context.Context, q store.DBTX, tenantID, indexID string, chunkIDs []string, vecs [][]float32) error {
	for i, id := range chunkIDs {
		if _, err := q.Exec(ctx, `INSERT INTO chunk_embeddings (chunk_id, index_id, tenant_id, embedding)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4::vector) ON CONFLICT (chunk_id, index_id) DO NOTHING`,
			id, indexID, tenantID, store.VecLiteral(vecs[i])); err != nil {
			return err
		}
	}
	return nil
}
