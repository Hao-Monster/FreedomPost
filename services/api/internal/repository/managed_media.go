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
	"github.com/google/uuid"
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
	// Shared media may appear in a different order in two articles. Lock all
	// referenced, claimed, and previously attached media by attachment ID first
	// so concurrent saves and deletes cannot form a lock cycle.
	candidateIDs := make([]string, 0, len(urls)+len(claimMap))
	candidateLegacyNames := make([]string, 0, len(urls))
	for _, claim := range claims {
		if _, err := uuid.Parse(claim.ID); err == nil {
			candidateIDs = append(candidateIDs, claim.ID)
		}
	}
	for _, raw := range urls {
		if _, id, ok := mediarefs.Parse(raw); ok {
			candidateIDs = append(candidateIDs, id)
		} else if !strings.HasPrefix(raw, mediarefs.Prefix) {
			if name := mediarefs.Filename(raw); name != "" {
				candidateLegacyNames = append(candidateLegacyNames, name)
			}
		}
	}
	if _, err := tx.Exec(ctx, `SELECT a.id FROM attachments a
	 WHERE a.id = ANY($1::uuid[])
	    OR (a.delivery_mode = 'legacy_public' AND a.stored_filename = ANY($2::text[]))
	    OR EXISTS (SELECT 1 FROM attachment_post_refs r WHERE r.attachment_id = a.id AND r.post_id = $3::uuid)
	 ORDER BY a.id FOR UPDATE`, candidateIDs, candidateLegacyNames, postID); err != nil {
		return fmt.Errorf("lock post media for save: %w", err)
	}
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
		var hasBodyReference bool
		err := tx.QueryRow(ctx, `SELECT delivery_mode, owner_type, owner_id, context_post_id,
		 upload_state, COALESCE(claim_token_hash,''), pending_until,
		 EXISTS(SELECT 1 FROM attachment_post_refs r WHERE r.attachment_id=a.id AND r.post_id=$2)
		 FROM attachments a WHERE id=$1 FOR UPDATE`, id, postID).
			Scan(&mode, &ownerType, &ownerID, &contextID, &state, &claimHash, &pending, &hasBodyReference)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidAttachment
		}
		if err != nil {
			return fmt.Errorf("lock article media: %w", err)
		}
		if mode != "managed" || state != "ready" {
			return domain.ErrInvalidAttachment
		}
		// Migration can preserve a comment attachment already embedded in this
		// article. Keep that reference without allowing post claims or new
		// article associations to take over comment uploads.
		if ownerType != "post" && (ownerType != "comment" || pending != nil || ownerID == nil || !hasBodyReference) {
			return domain.ErrInvalidAttachment
		}
		if pending != nil {
			if ownerID != nil || (contextID != nil && *contextID != postID) || !pending.After(time.Now()) || claimMap[id] == "" || !security.HMACEqual(claimHash, security.HashToken(claimMap[id])) {
				return domain.ErrInvalidAttachment
			}
			if _, err := tx.Exec(ctx, `UPDATE attachments SET owner_id=$2, context_post_id=COALESCE(context_post_id,$2), claim_token_hash=NULL, pending_until=NULL WHERE id=$1`, id, postID); err != nil {
				return err
			}
			usedClaims[id] = true
		} else if ownerID == nil {
			return domain.ErrInvalidAttachment
		}
		refs[id] = true
		canonical := domain.ManagedMediaURL(postID, id)
		if raw != canonical {
			rewrittenHTML := mediarefs.RewriteHTML(*rendered, raw, canonical)
			if strings.TrimSpace(*markdown) != "" {
				rewritten := mediarefs.RewriteMarkdown(*markdown, raw, canonical)
				if rewritten == *markdown {
					return domain.ErrInvalidAttachment
				}
				*markdown = rewritten
			} else if rewrittenHTML == *rendered {
				return domain.ErrInvalidAttachment
			}
			*rendered = rewrittenHTML
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
	legacyIdentities := map[string]bool{}
	legacyNames := []string{}
	for _, raw := range urls {
		if strings.HasPrefix(raw, mediarefs.Prefix) {
			continue
		}
		if id := mediarefs.LegacyIdentity(raw); id != "" {
			legacyIdentities[id] = true
		}
		if name := mediarefs.Filename(raw); name != "" {
			legacyNames = append(legacyNames, name)
		}
	}
	if len(legacyNames) > 0 {
		rows, err := tx.Query(ctx, `SELECT id, public_url, upload_state FROM attachments WHERE delivery_mode='legacy_public' AND stored_filename=ANY($1::text[]) ORDER BY id FOR UPDATE`, legacyNames)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, publicURL, uploadState string
			if err := rows.Scan(&id, &publicURL, &uploadState); err != nil {
				rows.Close()
				return err
			}
			if legacyIdentities[mediarefs.LegacyIdentity(publicURL)] {
				if uploadState != "ready" {
					rows.Close()
					return domain.ErrInvalidAttachment
				}
				refs[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	var previousRefs []string
	prevRows, err := tx.Query(ctx, `SELECT attachment_id FROM attachment_post_refs WHERE post_id=$1 ORDER BY attachment_id`, postID)
	if err != nil {
		return err
	}
	for prevRows.Next() {
		var id string
		if err := prevRows.Scan(&id); err != nil {
			prevRows.Close()
			return err
		}
		if !refs[id] {
			previousRefs = append(previousRefs, id)
		}
	}
	err = prevRows.Err()
	prevRows.Close()
	if err != nil {
		return err
	}
	if len(previousRefs) > 0 {
		if _, err := tx.Exec(ctx, `SELECT id FROM attachments WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, previousRefs); err != nil {
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
	return releaseUnreferencedAttachments(ctx, tx, previousRefs)
}

func releaseUnreferencedAttachments(ctx context.Context, tx pgx.Tx, attachmentIDs []string) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE attachments a SET pending_until=COALESCE(a.pending_until, NOW()+INTERVAL '24 hours')
	 WHERE a.id=ANY($1::uuid[]) AND a.owner_id IS NULL AND a.pending_until IS NULL
	 AND NOT EXISTS (SELECT 1 FROM attachment_post_refs r WHERE r.attachment_id=a.id)`, attachmentIDs)
	return err
}

func requireProtectedPostMedia(ctx context.Context, tx pgx.Tx, postID string, visibility domain.PostVisibility, rendered string) error {
	urls := mediarefs.URLs(rendered)
	identities := map[string]bool{}
	names := []string{}
	for _, raw := range urls {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, err := mediarefs.ParseURL(raw); err != nil {
			return domain.ErrMediaMigrationRequired
		}
		identity := mediarefs.LegacyIdentity(raw)
		if visibility != domain.VisibilityPublic && strings.HasPrefix(identity, "/api/uploads/") {
			return domain.ErrMediaMigrationRequired
		}
		identities[identity] = true
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
