// Package admin implements `devbrain admin ...` (runs as devbrain_owner via the
// devbrain-admin wrapper). Controlled reindex: create -> build -> activate -> drop.
package admin

import (
	"strings"

	"context"
	"devbrain/internal/learn"
	"errors"
	"flag"
	"fmt"
	"io"

	"devbrain/internal/embedsvc"
	"devbrain/internal/provider"
	"devbrain/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Admin struct {
	DB  *pgxpool.Pool
	Svc *embedsvc.Service
	Out io.Writer
}

const usage = `usage: devbrain admin <cmd>
  index list
  index create -name N -provider P -model M -dims D [-model-version V]
  index build N [-batch 64]
  index activate N [-force]
  index drop N
  tenant list
  tenant policy <tenant-name> external_allowed|local_only
  learn recipes <tenant-name>   (request→correction→solution detection over source=cursor docs; status unverified)`

func (a *Admin) Run(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New(usage)
	}
	switch args[0] + " " + args[1] {
	case "index list":
		return a.list(ctx)
	case "index create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		name, prov, model, ver := fs.String("name", "", ""), fs.String("provider", "", ""), fs.String("model", "", ""), fs.String("model-version", "", "")
		dims := fs.Int("dims", 0, "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return a.create(ctx, *name, *prov, *model, *ver, *dims)
	case "index build":
		fs := flag.NewFlagSet("build", flag.ContinueOnError)
		b := fs.Int("batch", 64, "")
		if len(args) < 3 {
			return errors.New(usage)
		}
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		return a.build(ctx, args[2], *b)
	case "index activate":
		fs := flag.NewFlagSet("activate", flag.ContinueOnError)
		force := fs.Bool("force", false, "")
		if len(args) < 3 {
			return errors.New(usage)
		}
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		return a.activate(ctx, args[2], *force)
	case "index drop":
		if len(args) < 3 {
			return errors.New(usage)
		}
		return a.drop(ctx, args[2])
	case "tenant list":
		return a.tenants(ctx)
	case "learn recipes":
		if len(args) < 3 {
			return errors.New(usage)
		}
		return a.learnRecipes(ctx, args[2])
	case "tenant policy":
		if len(args) < 4 {
			return errors.New(usage)
		}
		return a.policy(ctx, args[2], args[3])
	}
	return errors.New(usage)
}

func (a *Admin) list(ctx context.Context) error {
	idx, err := store.ListIndexes(ctx, a.DB)
	if err != nil {
		return err
	}
	for _, i := range idx {
		var n int
		_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM chunk_embeddings WHERE index_id=$1::uuid", i.ID).Scan(&n)
		fmt.Fprintf(a.Out, "%-24s %-9s %s/%s v=%q dims=%d vectors=%d\n", i.Name, i.Status, i.Provider, i.Model, i.ModelVersion, i.Dims, n)
	}
	return nil
}

func (a *Admin) create(ctx context.Context, name, prov, model, ver string, dims int) error {
	if name == "" || prov == "" || model == "" || dims <= 0 {
		return errors.New(usage)
	}
	if ex, err := store.IndexByName(ctx, a.DB, name); err != nil {
		return err
	} else if ex != nil {
		fmt.Fprintf(a.Out, "index %s already exists (%s)\n", name, ex.Status)
		return nil
	}
	if _, err := a.Svc.Router.Get(prov); err != nil {
		return fmt.Errorf("provider %q not configured", prov)
	}
	var id string
	if err := a.DB.QueryRow(ctx, `INSERT INTO embedding_indexes (name, provider, model, model_version, dims)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text`, name, prov, model, ver, dims).Scan(&id); err != nil {
		return err
	}
	if dims <= 2000 { // pgvector HNSW limit for vector type
		sql := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON chunk_embeddings USING hnsw ((embedding::vector(%d)) vector_cosine_ops) WHERE index_id = '%s'`,
			store.HNSWIndexName(id), dims, id)
		if _, err := a.DB.Exec(ctx, sql); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(a.Out, "WARN dims>2000: no HNSW index, exact search only")
	}
	fmt.Fprintf(a.Out, "created index %s (%s) status=building\n", name, id)
	return nil
}

func (a *Admin) build(ctx context.Context, name string, b int) error {
	idx, err := store.IndexByName(ctx, a.DB, name)
	if err != nil || idx == nil {
		return fmt.Errorf("index %q not found: %v", name, err)
	}
	rows, err := a.DB.Query(ctx, "SELECT id::text, name FROM tenants ORDER BY name")
	if err != nil {
		return err
	}
	type t struct{ id, name string }
	var ts []t
	for rows.Next() {
		var x t
		if err := rows.Scan(&x.id, &x.name); err != nil {
			return err
		}
		ts = append(ts, x)
	}
	rows.Close()
	for _, tn := range ts {
		pol, err := store.TenantPolicy(ctx, a.DB, tn.id)
		if err != nil {
			return err
		}
		done, hits := 0, 0
		for {
			r, err := a.DB.Query(ctx, `SELECT c.id::text, c.content FROM chunks c WHERE c.tenant_id=$1::uuid
				AND NOT EXISTS (SELECT 1 FROM chunk_embeddings ce WHERE ce.chunk_id=c.id AND ce.index_id=$2::uuid)
				ORDER BY c.id LIMIT $3`, tn.id, idx.ID, b)
			if err != nil {
				return err
			}
			ids, texts, err := collect(r)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			res, err := a.Svc.Embed(ctx, a.DB, tn.id, pol, idx, texts)
			if errors.Is(err, provider.ErrPolicy) {
				fmt.Fprintf(a.Out, "tenant %s: SKIPPED (policy %s forbids %s)\n", tn.name, pol, idx.Provider)
				break
			}
			if err != nil {
				return err
			}
			if err := embedsvc.StoreChunkEmbeddings(ctx, a.DB, tn.id, idx.ID, ids, res.Vectors); err != nil {
				return err
			}
			done += len(ids)
			hits += res.CacheHits
		}
		fmt.Fprintf(a.Out, "tenant %s: embedded %d chunks (cache hits %d)\n", tn.name, done, hits)
	}
	return nil
}

func collect(r pgx.Rows) ([]string, []string, error) {
	defer r.Close()
	var ids, texts []string
	for r.Next() {
		var id, c string
		if err := r.Scan(&id, &c); err != nil {
			return nil, nil, err
		}
		ids, texts = append(ids, id), append(texts, c)
	}
	return ids, texts, r.Err()
}

// pending = chunks without a vector in idx, excluding tenants whose policy forbids the provider.
func (a *Admin) pending(ctx context.Context, idx *store.Index) (int, error) {
	e, err := a.Svc.Router.Get(idx.Provider)
	if err != nil {
		return 0, err
	}
	excl := ""
	if e.External() {
		excl = "AND c.tenant_id NOT IN (SELECT tenant_id FROM tenant_settings WHERE provider_policy='local_only')"
	}
	var n int
	err = a.DB.QueryRow(ctx, `SELECT count(*) FROM chunks c WHERE NOT EXISTS
		(SELECT 1 FROM chunk_embeddings ce WHERE ce.chunk_id=c.id AND ce.index_id=$1::uuid) `+excl, idx.ID).Scan(&n)
	return n, err
}

func (a *Admin) activate(ctx context.Context, name string, force bool) error {
	idx, err := store.IndexByName(ctx, a.DB, name)
	if err != nil || idx == nil {
		return fmt.Errorf("index %q not found: %v", name, err)
	}
	if idx.Status == "active" {
		fmt.Fprintln(a.Out, "already active")
		return nil
	}
	n, err := a.pending(ctx, idx)
	if err != nil {
		return err
	}
	if n > 0 && !force {
		return fmt.Errorf("index %s has %d chunks pending; run build first (or -force)", name, n)
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE embedding_indexes SET status='retired', retired_at=now() WHERE status='active'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE embedding_indexes SET status='active', activated_at=now(), retired_at=NULL WHERE id=$1::uuid", idx.ID); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "activated %s (previous active, if any, is now retired and kept)\n", name)
		return nil
	})
}

func (a *Admin) drop(ctx context.Context, name string) error {
	idx, err := store.IndexByName(ctx, a.DB, name)
	if err != nil || idx == nil {
		return fmt.Errorf("index %q not found: %v", name, err)
	}
	if idx.Status == "active" {
		return errors.New("refusing to drop the active index; activate another first")
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DROP INDEX IF EXISTS "+store.HNSWIndexName(idx.ID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM embedding_indexes WHERE id=$1::uuid", idx.ID); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "dropped %s and its vectors (cache kept)\n", name)
		return nil
	})
}

func (a *Admin) tenants(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, `SELECT t.name, COALESCE(s.provider_policy,'external_allowed'),
		(SELECT count(*) FROM documents d WHERE d.tenant_id=t.id), (SELECT count(*) FROM chunks c WHERE c.tenant_id=t.id)
		FROM tenants t LEFT JOIN tenant_settings s ON s.tenant_id=t.id ORDER BY t.name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n, p string
		var d, c int
		if err := rows.Scan(&n, &p, &d, &c); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "%-20s %-17s docs=%d chunks=%d\n", n, p, d, c)
	}
	return rows.Err()
}

func (a *Admin) policy(ctx context.Context, tenantName, pol string) error {
	if pol != string(provider.PolicyExternalAllowed) && pol != string(provider.PolicyLocalOnly) {
		return errors.New(usage)
	}
	tag, err := a.DB.Exec(ctx, `INSERT INTO tenant_settings (tenant_id, provider_policy)
		SELECT id, $2 FROM tenants WHERE name=$1
		ON CONFLICT (tenant_id) DO UPDATE SET provider_policy=EXCLUDED.provider_policy, updated_at=now()`, tenantName, pol)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tenant %q not found", tenantName)
	}
	fmt.Fprintf(a.Out, "tenant %s policy=%s\n", tenantName, pol)
	return nil
}

func (a *Admin) learnRecipes(ctx context.Context, tenantName string) error {
	var tid string
	if err := a.DB.QueryRow(ctx, "SELECT id::text FROM tenants WHERE name=$1", tenantName).Scan(&tid); err != nil {
		return fmt.Errorf("tenant %q: %w", tenantName, err)
	}
	rows, err := a.DB.Query(ctx, `SELECT COALESCE(d.metadata->>'conversation_id',''), COALESCE(d.metadata->>'project',''), d.id::text,
		string_agg(c.content, E'\n\n' ORDER BY c.ord)
		FROM documents d JOIN chunks c ON c.document_id=d.id WHERE d.tenant_id=$1::uuid AND d.source='cursor'
		GROUP BY d.id ORDER BY 1, COALESCE((d.metadata->>'part')::int,1)`, tid)
	if err != nil {
		return err
	}
	type conv struct {
		project string
		docs    []string
		text    strings.Builder
	}
	convs := map[string]*conv{}
	var order []string
	for rows.Next() {
		var cid, proj, did, txt string
		if err := rows.Scan(&cid, &proj, &did, &txt); err != nil {
			rows.Close()
			return err
		}
		c, ok := convs[cid]
		if !ok {
			c = &conv{project: proj}
			convs[cid] = c
			order = append(order, cid)
		}
		c.docs = append(c.docs, did)
		c.text.WriteString(txt + "\n\n")
	}
	rows.Close()
	var found, created, dup, verified int
	for _, cid := range order {
		c := convs[cid]
		for _, r := range learn.Detect(learn.ParseConversation(c.text.String())) {
			found++
			proj := c.project
			if strings.HasPrefix(proj, "(") {
				proj = ""
			}
			refs := []map[string]any{{"conversation_id": cid, "document_ids": c.docs, "messages": []int{r.StartMsg, r.EndMsg}}}
			res, err := learn.Save(ctx, a.DB, tid, learn.SaveInput{Recipe: r, Project: proj, ConversationID: cid, CreatedBy: "learn", Refs: refs})
			if err != nil {
				return err
			}
			switch {
			case res.Duplicate:
				dup++
			case res.Created:
				created++
			}
			if res.Status == "verified" {
				verified++
			}
		}
	}
	fmt.Fprintf(a.Out, "conversations=%d sequences=%d created=%d merged_duplicates=%d verified=%d (new recipes are unverified)\n",
		len(order), found, created, dup, verified)
	return nil
}
