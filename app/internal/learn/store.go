package learn

import (
	"context"
	"encoding/json"
	"errors"

	"devbrain/internal/store"

	"github.com/jackc/pgx/v5"
)

// SaveInput carries a recipe plus provenance. Status is "verified" ONLY when evidence AND approver are both given.
type SaveInput struct {
	Recipe
	Project, ConversationID, Evidence, ApprovedBy, CreatedBy string
	Refs                                                     []map[string]any
}

type SaveResult struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
	Created   bool   `json:"created"`
}

func StatusFor(evidence, approvedBy string) string {
	if evidence != "" && approvedBy != "" {
		return "verified"
	}
	return "unverified"
}

// Save inserts or merges (exact fingerprint, or ≥0.8 title-word Jaccard within the project) a recipe. Caller sets tenant scope (RLS) or BYPASSRLS.
func Save(ctx context.Context, q store.DBTX, tenantID string, in SaveInput) (SaveResult, error) {
	fp := Fingerprint(in.Project, in.Recipe)
	var id, st string
	err := q.QueryRow(ctx, `SELECT id::text, status FROM recipes WHERE tenant_id=$1::uuid AND fingerprint=$2`, tenantID, fp).Scan(&id, &st)
	if errors.Is(err, pgx.ErrNoRows) {
		rows, err2 := q.Query(ctx, `SELECT id::text, status, title FROM recipes WHERE tenant_id=$1::uuid AND project=$2`, tenantID, in.Project)
		if err2 != nil {
			return SaveResult{}, err2
		}
		w := Words(in.Title)
		for rows.Next() {
			var rid, rst, t string
			if rows.Scan(&rid, &rst, &t) == nil && Jaccard(w, Words(t)) >= 0.8 {
				id, st = rid, rst
				break
			}
		}
		rows.Close()
	} else if err != nil {
		return SaveResult{}, err
	}
	status := StatusFor(in.Evidence, in.ApprovedBy)
	if id != "" { // duplicate: merge provenance, never downgrade a verified recipe
		if st == "verified" {
			status = "verified"
		}
		_, err := q.Exec(ctx, `UPDATE recipes SET occurrences=occurrences+1,
			source_conversation_ids = COALESCE((SELECT array_agg(DISTINCT x) FROM unnest(source_conversation_ids || $2::text[]) x), '{}'),
			status=$3::text, evidence=CASE WHEN $4::text<>'' THEN $4::text ELSE evidence END,
			approved_by=CASE WHEN $5::text<>'' THEN $5::text ELSE approved_by END, updated_at=now()
			WHERE id=$1::uuid`, id, nonEmpty(in.ConversationID), status, in.Evidence, in.ApprovedBy)
		return SaveResult{ID: id, Status: status, Duplicate: true}, err
	}
	v, _ := json.Marshal(orEmpty(in.Versions))
	refs, _ := json.Marshal(in.Refs)
	if in.Refs == nil {
		refs = []byte("[]")
	}
	if in.Files == nil {
		in.Files = []string{}
	}
	if in.Commits == nil {
		in.Commits = []string{}
	}
	err = q.QueryRow(ctx, `INSERT INTO recipes (tenant_id, project, title, problem, solution, code, versions, files, commits, corrections,
		source_conversation_ids, source_refs, fingerprint, status, evidence, approved_by, created_by)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16,$17) RETURNING id::text`,
		tenantID, in.Project, in.Title, in.Problem, in.Solution, in.Code, string(v), in.Files, in.Commits, in.Corrections,
		nonEmpty(in.ConversationID), string(refs), fp, status, in.Evidence, in.ApprovedBy, in.CreatedBy).Scan(&id)
	return SaveResult{ID: id, Status: status, Created: true}, err
}

func nonEmpty(s string) []string {
	if s == "" {
		return []string{}
	}
	return []string{s}
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
