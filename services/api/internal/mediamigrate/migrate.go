// Package mediamigrate implements the explicitly invoked historical-media
// migration. Audit is read-only; copies, retirement and purge acknowledgement
// are separate operations so a deployment never deletes production objects.
package mediamigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediarefs"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/renderer"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Plan struct {
	Items             []Item   `json:"items"`
	UnresolvedURLs    []string `json:"unresolvedURLs"`
	PendingRetirement int      `json:"pendingRetirement"`
	PendingCachePurge int      `json:"pendingCachePurge"`
}

type Item struct {
	ID        string   `json:"attachmentID"`
	Provider  string   `json:"provider"`
	PostIDs   []string `json:"postIDs"`
	Blocked   string   `json:"blocked,omitempty"`
	URL       string   `json:"-"`
	Key       string   `json:"-"`
	Name      string   `json:"-"`
	OwnerType string   `json:"-"`
	OwnerID   *string  `json:"-"`
	Size      int64    `json:"-"`
	SHA       string   `json:"-"`
}

type postSnapshot struct {
	id, markdown, html string
	updated            time.Time
}

func readPosts(ctx context.Context, db *pgxpool.Pool) ([]postSnapshot, error) {
	rows, err := db.Query(ctx, `SELECT id,content_markdown,content_html,updated_at FROM posts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []postSnapshot
	for rows.Next() {
		var p postSnapshot
		if err := rows.Scan(&p.id, &p.markdown, &p.html, &p.updated); err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

func Audit(ctx context.Context, db *pgxpool.Pool) (*Plan, error) {
	posts, err := readPosts(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT id,storage_provider,storage_key,public_url,original_filename,owner_type,owner_id,size_bytes,COALESCE(sha256,'') FROM attachments WHERE delivery_mode='legacy_public' AND upload_state='ready' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var candidates []Item
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Provider, &item.Key, &item.URL, &item.Name, &item.OwnerType, &item.OwnerID, &item.Size, &item.SHA); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	plan := &Plan{Items: []Item{}, UnresolvedURLs: []string{}}
	known := map[string]bool{}
	for _, item := range candidates {
		known[mediarefs.LegacyIdentity(item.URL)] = true
		for _, post := range posts {
			for _, raw := range mediarefs.URLs(post.html) {
				if mediarefs.LegacyIdentity(raw) == mediarefs.LegacyIdentity(item.URL) && raw != "" {
					item.PostIDs = append(item.PostIDs, post.id)
					break
				}
			}
		}
		if item.OwnerType == "comment" && item.OwnerID != nil {
			var postID string
			err := db.QueryRow(ctx, `SELECT post_id FROM comments WHERE id=$1`, *item.OwnerID).Scan(&postID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				item.PostIDs = appendUnique(item.PostIDs, postID)
			}
		}
		if len(item.PostIDs) == 0 {
			continue
		}
		publicUse, err := catalogUses(ctx, db, item.URL)
		if err != nil {
			return nil, err
		}
		if publicUse {
			item.Blocked = "also used by a product/tool; split that public reference before migration"
		}
		if item.OwnerType != "post" && item.OwnerType != "comment" {
			item.Blocked = "unsupported owner type"
		}
		plan.Items = append(plan.Items, item)
	}
	for _, post := range posts {
		for _, raw := range mediarefs.ObjectURLs(post.html) {
			if _, _, ok := mediarefs.Parse(raw); !ok && !known[mediarefs.LegacyIdentity(raw)] {
				plan.UnresolvedURLs = appendUnique(plan.UnresolvedURLs, raw)
			}
		}
		for _, raw := range mediarefs.URLs(post.html) {
			if _, _, ok := mediarefs.Parse(raw); ok || known[mediarefs.LegacyIdentity(raw)] {
				continue
			}
			// Report local unmanaged object links without enumerating storage. Other
			// external URLs may be normal links; operators review those in the report.
			if strings.HasPrefix(raw, "/api/uploads/") {
				plan.UnresolvedURLs = appendUnique(plan.UnresolvedURLs, raw)
			}
		}
	}
	err = db.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE retired_at IS NULL),COUNT(*) FILTER(WHERE cache_purged_at IS NULL) FROM managed_media_legacy_sources`).Scan(&plan.PendingRetirement, &plan.PendingCachePurge)
	return plan, err
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}

// Copy rewrites every article reference to each selected object. It verifies a
// private copy before committing metadata and keeps the old public object.
// A failure may leave an extra private copy, but never exposes or deletes data.
func Copy(ctx context.Context, db *pgxpool.Pool, source map[string]storage.ManagedAdapter, target storage.ManagedAdapter) error {
	plan, err := Audit(ctx, db)
	if err != nil {
		return err
	}
	if len(plan.UnresolvedURLs) > 0 {
		return fmt.Errorf("audit contains untracked upload URLs; resolve them before apply")
	}
	for _, item := range plan.Items {
		if item.Blocked != "" {
			return fmt.Errorf("attachment %s is blocked: %s", item.ID, item.Blocked)
		}
	}
	for _, item := range plan.Items {
		store := source[item.Provider]
		if store == nil {
			return fmt.Errorf("source storage %s is not configured", item.Provider)
		}
		object, err := store.Open(ctx, item.Key, http.MethodGet, "")
		if err != nil {
			return fmt.Errorf("read source %s: %w", item.ID, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(object.Body, 500*1024*1024+1))
		object.Body.Close()
		if readErr != nil {
			return readErr
		}
		if int64(len(data)) != item.Size || item.SHA != "" && storage.ComputeSHA256(data) != item.SHA {
			return fmt.Errorf("source checksum/size mismatch for %s", item.ID)
		}
		copy, err := target.Put(ctx, storage.PutInput{Data: data, OriginalName: item.Name, OwnerType: item.OwnerType})
		if err != nil {
			return err
		}
		check, err := target.Open(ctx, copy.StorageKey, http.MethodGet, "")
		if err != nil {
			return err
		}
		copied, readErr := io.ReadAll(io.LimitReader(check.Body, int64(len(data))+1))
		check.Body.Close()
		if readErr != nil || storage.ComputeSHA256(copied) != storage.ComputeSHA256(data) {
			return fmt.Errorf("private copy verification failed for %s", item.ID)
		}
		if err := commitCopy(ctx, db, item, copy); err != nil {
			// COMMIT can succeed while its response is lost. Retain the verified
			// copy rather than deleting an object the database may already use.
			return fmt.Errorf("copy %s retained; inspect migration state before retry: %w", item.ID, err)
		}
	}
	return nil
}

func commitCopy(ctx context.Context, db *pgxpool.Pool, item Item, copy *storage.StoredObject) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Match the application's post-before-attachment lock order. The CLI also
	// requires a maintenance window to prevent new references appearing mid-run.
	rows, err := tx.Query(ctx, `SELECT id,content_markdown,content_html,updated_at FROM posts ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	var posts []postSnapshot
	for rows.Next() {
		var post postSnapshot
		if err := rows.Scan(&post.id, &post.markdown, &post.html, &post.updated); err != nil {
			rows.Close()
			return err
		}
		posts = append(posts, post)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var key, mode, publicURL string
	err = tx.QueryRow(ctx, `SELECT storage_key,delivery_mode,public_url FROM attachments WHERE id=$1 FOR UPDATE`, item.ID).Scan(&key, &mode, &publicURL)
	if err != nil {
		return err
	}
	if mode != "legacy_public" || key != item.Key || publicURL != item.URL {
		return fmt.Errorf("attachment changed since audit: %s", item.ID)
	}
	publicUse, err := catalogUses(ctx, tx, item.URL)
	if err != nil {
		return err
	}
	if publicUse {
		return fmt.Errorf("public product/tool reference appeared during migration")
	}
	refs := []string{}
	for _, post := range posts {
		markdown := post.markdown
		for _, raw := range mediarefs.URLs(post.html) {
			if mediarefs.LegacyIdentity(raw) == mediarefs.LegacyIdentity(item.URL) {
				markdown = mediarefs.RewriteMarkdown(markdown, raw, domain.ManagedMediaURL(post.id, item.ID))
			}
		}
		if markdown == post.markdown {
			for _, raw := range mediarefs.URLs(post.html) {
				if mediarefs.LegacyIdentity(raw) == mediarefs.LegacyIdentity(item.URL) {
					return fmt.Errorf("HTML-only media reference requires manual migration: %s", post.id)
				}
			}
			continue
		}
		refs = append(refs, post.id)
		rendered := renderer.Render(markdown)
		if _, err := tx.Exec(ctx, `UPDATE posts SET content_markdown=$2,content_html=$3,search_text=$4,excerpt=$5,updated_at=NOW() WHERE id=$1`, post.id, markdown, rendered.HTML, rendered.SearchText, rendered.Excerpt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO attachment_post_refs(attachment_id,post_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, item.ID, post.id); err != nil {
			return err
		}
	}
	var contextID string
	ownerID := item.OwnerID
	if item.OwnerType == "comment" && item.OwnerID != nil {
		if err := tx.QueryRow(ctx, `SELECT post_id FROM comments WHERE id=$1`, *item.OwnerID).Scan(&contextID); err != nil {
			return err
		}
	} else if len(refs) > 0 {
		contextID = refs[0]
		ownerID = &contextID
	} else {
		return fmt.Errorf("attachment has no current article reference")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO managed_media_legacy_sources(attachment_id,storage_provider,storage_key,public_url,sha256) VALUES($1,$2,$3,$4,$5)`, item.ID, item.Provider, item.Key, item.URL, copy.SHA256); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE attachments SET delivery_mode='managed',storage_provider=$2,storage_key=$3,stored_filename=$4,public_url='',sha256=$5,mime_type=$6,detected_mime_type=$6,owner_id=$7,context_post_id=$8,pending_until=NULL,claim_token_hash=NULL WHERE id=$1`, item.ID, copy.StorageProvider, copy.StorageKey, copy.StoredFilename, copy.SHA256, copy.MimeType, ownerID, contextID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Retire is destructive and is never called by Copy or Audit. The command must
// require an explicit flag and prior backup/rollback approval from the operator.
func Retire(ctx context.Context, db *pgxpool.Pool, source, private map[string]storage.ManagedAdapter) error {
	rows, err := db.Query(ctx, `SELECT old.attachment_id,old.storage_provider,old.storage_key,old.public_url,old.sha256,a.storage_provider,a.storage_key,a.delivery_mode
	 FROM managed_media_legacy_sources old JOIN attachments a ON a.id=old.attachment_id WHERE old.retired_at IS NULL ORDER BY old.attachment_id`)
	if err != nil {
		return err
	}
	type old struct{ id, provider, key, url, sha, targetProvider, targetKey, mode string }
	var items []old
	for rows.Next() {
		var i old
		if err := rows.Scan(&i.id, &i.provider, &i.key, &i.url, &i.sha, &i.targetProvider, &i.targetKey, &i.mode); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		store := source[i.provider]
		if store == nil {
			return fmt.Errorf("source provider %s unavailable", i.provider)
		}
		publicUse, err := catalogUses(ctx, db, i.url)
		if err != nil {
			return err
		}
		var duplicate bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE delivery_mode='legacy_public' AND storage_provider=$1 AND storage_key=$2)`, i.provider, i.key).Scan(&duplicate); err != nil {
			return err
		}
		posts, err := readPosts(ctx, db)
		if err != nil {
			return err
		}
		for _, post := range posts {
			for _, raw := range mediarefs.URLs(post.html) {
				if mediarefs.LegacyIdentity(raw) == mediarefs.LegacyIdentity(i.url) {
					publicUse = true
				}
			}
		}
		if publicUse || duplicate {
			return fmt.Errorf("original %s has a current reference; refusing retirement", i.id)
		}
		target := private[i.targetProvider]
		if i.mode != "managed" || target == nil {
			return fmt.Errorf("private copy is unavailable for %s", i.id)
		}
		check, err := target.Open(ctx, i.targetKey, http.MethodGet, "")
		if err != nil {
			return err
		}
		bytes, readErr := io.ReadAll(io.LimitReader(check.Body, 500*1024*1024+1))
		check.Body.Close()
		if readErr != nil || storage.ComputeSHA256(bytes) != i.sha {
			return fmt.Errorf("private copy checksum failed before retirement: %s", i.id)
		}
		if err := store.Delete(ctx, i.key); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `UPDATE managed_media_legacy_sources SET retired_at=NOW() WHERE attachment_id=$1`, i.id); err != nil {
			return err
		}
	}
	return nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func catalogUses(ctx context.Context, db queryer, rawURL string) (bool, error) {
	rows, err := db.Query(ctx, `SELECT to_jsonb(p)::text FROM products p UNION ALL SELECT to_jsonb(t)::text FROM tools t`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	identity := mediarefs.LegacyIdentity(rawURL)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return false, err
		}
		for _, value := range fields {
			if value, ok := value.(string); ok {
				if mediarefs.LegacyIdentity(value) == identity {
					return true, nil
				}
				for _, url := range mediarefs.URLs(renderer.Render(value).HTML) {
					if mediarefs.LegacyIdentity(url) == identity {
						return true, nil
					}
				}
			}
		}
	}
	return false, rows.Err()
}

// ConfirmPurge records an external CDN/browser-policy gate only after signed
// origin reads prove the original objects are gone. It cannot purge a CDN.
func ConfirmPurge(ctx context.Context, db *pgxpool.Pool, source map[string]storage.ManagedAdapter) error {
	rows, err := db.Query(ctx, `SELECT attachment_id,storage_provider,storage_key FROM managed_media_legacy_sources WHERE cache_purged_at IS NULL`)
	if err != nil {
		return err
	}
	type old struct{ id, provider, key string }
	var items []old
	for rows.Next() {
		var i old
		if err := rows.Scan(&i.id, &i.provider, &i.key); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		store := source[i.provider]
		if store == nil {
			return fmt.Errorf("source provider unavailable")
		}
		object, err := store.Open(ctx, i.key, http.MethodHead, "")
		if object != nil && object.Body != nil {
			object.Body.Close()
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("original object absence is not verified: %s", i.id)
		}
		result, err := db.Exec(ctx, `UPDATE managed_media_legacy_sources SET cache_purged_at=NOW() WHERE attachment_id=$1 AND retired_at IS NOT NULL`, i.id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("retirement has not been recorded: %s", i.id)
		}
	}
	return nil
}
