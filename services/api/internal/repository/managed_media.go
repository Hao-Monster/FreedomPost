package repository

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediarefs"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/jackc/pgx/v5"
)

func (p *Postgres) GetManagedAttachment(ctx context.Context, id string) (*domain.Attachment, error) {
	var a domain.Attachment
	err := p.pool.QueryRow(ctx, `SELECT id, owner_type, owner_id, original_filename,
	 stored_filename, storage_provider, storage_key, public_url, mime_type,
	 size_bytes, COALESCE(sha256,''), delivery_mode, context_post_id, pending_until,
	 upload_state, created_at FROM attachments WHERE id=$1`, id).Scan(
		&a.ID, &a.OwnerType, &a.OwnerID, &a.OriginalFilename, &a.StoredFilename,
		&a.StorageProvider, &a.StorageKey, &a.PublicURL, &a.MimeType, &a.SizeBytes,
		&a.SHA256, &a.DeliveryMode, &a.ContextPostID, &a.PendingUntil, &a.UploadState, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *Postgres) HasPostAttachmentReference(ctx context.Context, postID, attachmentID string) (bool, error) {
	var found bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS (
	 SELECT 1 FROM attachment_post_refs WHERE attachment_id=$1 AND post_id=$2
	 UNION ALL
	 SELECT 1 FROM attachments a JOIN comments c ON a.owner_id=c.id
	 WHERE a.id=$1 AND a.owner_type='comment' AND c.post_id=$2
	 AND a.context_post_id=c.post_id AND a.pending_until IS NULL AND a.upload_state='ready'
	)`, attachmentID, postID).Scan(&found)
	return found, err
}

// syncPostMedia runs inside the post transaction. A staged upload requires its
// one-use claim and original post context; already committed post media can be
// reused by the administrator without making a different article public.
func syncPostMedia(ctx context.Context, tx pgx.Tx, postID string, markdown, rendered *string, claims []domain.PendingPostImage) error {
	claimMap := make(map[string]string, len(claims))
	for _, claim := range claims {
		if claim.ID == "" || claim.ClaimToken == "" || claimMap[claim.ID] != "" {
			return domain.ErrInvalidAttachment
		}
		claimMap[claim.ID] = claim.ClaimToken
	}
	usedClaims := map[string]bool{}
	refs := map[string]bool{}
	urls := mediarefs.URLs(*rendered)
	// Shared media may appear in a different order in two articles. Lock by
	// attachment ID consistently so concurrent saves cannot form a lock cycle.
	sort.Slice(urls, func(i, j int) bool {
		_, a, _ := mediarefs.Parse(urls[i])
		_, b, _ := mediarefs.Parse(urls[j])
		return a < b
	})
	for _, raw := range urls {
		if !strings.HasPrefix(raw, mediarefs.Prefix) {
			continue
		}
		_, id, valid := mediarefs.Parse(raw)
		if !valid {
			return domain.ErrInvalidAttachment
		}
		var mode, ownerType, state, claimHash string
		var contextID, ownerID *string
		var pending *time.Time
		err := tx.QueryRow(ctx, `SELECT delivery_mode, owner_type, owner_id, context_post_id,
		 upload_state, COALESCE(claim_token_hash,''), pending_until FROM attachments WHERE id=$1 FOR UPDATE`, id).
			Scan(&mode, &ownerType, &ownerID, &contextID, &state, &claimHash, &pending)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidAttachment
		}
		if err != nil {
			return fmt.Errorf("lock article media: %w", err)
		}
		if mode != "managed" || ownerType != "post" || state != "ready" {
			return domain.ErrInvalidAttachment
		}
		if pending != nil {
			if ownerID != nil || contextID == nil || *contextID != postID || !pending.After(time.Now()) || claimMap[id] == "" || !security.HMACEqual(claimHash, security.HashToken(claimMap[id])) {
				return domain.ErrInvalidAttachment
			}
			if _, err := tx.Exec(ctx, `UPDATE attachments SET owner_id=$2, claim_token_hash=NULL, pending_until=NULL WHERE id=$1`, id, postID); err != nil {
				return err
			}
			usedClaims[id] = true
		} else if ownerID == nil {
			return domain.ErrInvalidAttachment
		}
		refs[id] = true
		canonical := domain.ManagedMediaURL(postID, id)
		if raw != canonical {
			rewritten := mediarefs.RewriteMarkdown(*markdown, raw, canonical)
			if rewritten == *markdown {
				return domain.ErrInvalidAttachment
			}
			*markdown = rewritten
			*rendered = mediarefs.RewriteHTML(*rendered, raw, canonical)
		}
	}
	// A previously staged legacy import may still be saved during migration.
	// It remains legacy_public and is never treated as protected merely by claim.
	claimIDs := make([]string, 0, len(claimMap))
	for id := range claimMap {
		claimIDs = append(claimIDs, id)
	}
	sort.Strings(claimIDs)
	for _, id := range claimIDs {
		token := claimMap[id]
		if usedClaims[id] {
			continue
		}
		var publicURL string
		err := tx.QueryRow(ctx, `SELECT public_url FROM attachments WHERE id=$1 AND delivery_mode='legacy_public'
		 AND owner_type='post' AND owner_id IS NULL AND uploader_type='admin-image-import'
		 AND claim_token_hash=$2 AND pending_until>NOW() AND upload_state='ready' FOR UPDATE`, id, security.HashToken(token)).Scan(&publicURL)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidAttachment
		}
		if err != nil {
			return err
		}
		if !strings.Contains(*rendered, `src="`+html.EscapeString(publicURL)+`"`) {
			return domain.ErrInvalidAttachment
		}
		if _, err := tx.Exec(ctx, `UPDATE attachments SET owner_id=$2, claim_token_hash=NULL, pending_until=NULL WHERE id=$1`, id, postID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM attachment_post_refs WHERE post_id=$1`, postID); err != nil {
		return err
	}
	for id := range refs {
		if _, err := tx.Exec(ctx, `INSERT INTO attachment_post_refs(attachment_id,post_id) VALUES($1,$2)`, id, postID); err != nil {
			return err
		}
	}
	return nil
}

func requireProtectedPostMedia(ctx context.Context, tx pgx.Tx, postID string, visibility domain.PostVisibility, rendered string) error {
	urls := mediarefs.URLs(rendered)
	identities := map[string]bool{}
	names := []string{}
	for _, raw := range urls {
		identities[mediarefs.LegacyIdentity(raw)] = true
		names = append(names, mediarefs.Filename(raw))
	}
	// Filename prefilter bounds this lookup while comparison uses the decoded
	// path and normalized host, not attacker-controlled query/fragment aliases.
	rows, err := tx.Query(ctx, `SELECT public_url FROM managed_media_legacy_sources WHERE regexp_replace(storage_key,'^.*/','')=ANY($1::text[])
	 UNION ALL SELECT public_url FROM attachments WHERE $2 AND delivery_mode='legacy_public' AND stored_filename=ANY($1::text[])`, names, visibility != domain.VisibilityPublic)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if identities[mediarefs.LegacyIdentity(raw)] {
			rows.Close()
			return domain.ErrMediaMigrationRequired
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if visibility == domain.VisibilityPublic {
		return nil
	}
	ids := []string{}
	for _, raw := range urls {
		if _, id, ok := mediarefs.Parse(raw); ok {
			ids = append(ids, id)
		}
	}
	var exposed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
	 SELECT 1 FROM attachments a JOIN comments c ON a.owner_type='comment' AND a.owner_id=c.id
	 WHERE c.post_id=$1 AND a.delivery_mode='legacy_public'
	 UNION ALL
	 SELECT 1 FROM managed_media_legacy_sources old
	 WHERE old.cache_purged_at IS NULL AND (
	 old.attachment_id=ANY($2::uuid[])
	 OR EXISTS(SELECT 1 FROM attachments a JOIN comments c ON a.owner_type='comment' AND a.owner_id=c.id WHERE a.id=old.attachment_id AND c.post_id=$1))
	)`, postID, ids).Scan(&exposed)
	if err != nil {
		return err
	}
	if exposed {
		return domain.ErrMediaMigrationRequired
	}
	return nil
}
