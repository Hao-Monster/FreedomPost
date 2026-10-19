package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type attachmentQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertAttachment(ctx context.Context, q attachmentQueryer, a domain.Attachment) (*domain.Attachment, error) {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	a.CreatedAt = time.Now()
	a.DeliveryMode = coalesce(a.DeliveryMode, "legacy_public")
	a.UploadState = coalesce(a.UploadState, "ready")
	err := q.QueryRow(ctx, `INSERT INTO attachments
		(id,owner_type,owner_id,uploader_type,original_filename,stored_filename,storage_provider,
		 storage_key,public_url,mime_type,detected_mime_type,size_bytes,sha256,claim_token_hash,
		 pending_until,source_host,created_at,delivery_mode,context_post_id,upload_state,uploader_ip_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING id`,
		a.ID, a.OwnerType, a.OwnerID, coalesce(a.UploaderType, "admin"), a.OriginalFilename, a.StoredFilename, a.StorageProvider,
		a.StorageKey, a.PublicURL, a.MimeType, a.DetectedMime, a.SizeBytes, a.SHA256, nullStr(a.ClaimTokenHash),
		a.PendingUntil, nullStr(a.SourceHost), a.CreatedAt, a.DeliveryMode, a.ContextPostID, a.UploadState, nullStr(a.UploaderIPHash)).Scan(&a.ID)
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	return &a, nil
}

func (p *Postgres) ReserveCommentAttachment(ctx context.Context, a domain.Attachment, ipLimit, globalLimit int64) (*domain.Attachment, error) {
	if a.OwnerType != "comment" || a.OwnerID != nil || a.UploaderType != "public-comment" || a.UploaderIPHash == "" || a.SizeBytes < 0 || a.PendingUntil == nil || a.ContextPostID == nil || a.DeliveryMode != "managed" || a.UploadState != "uploading" {
		return nil, domain.ErrInvalidAttachment
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// One short transaction serializes reservations across API instances. No
	// storage I/O occurs while holding this lock. Expired/deleting bytes count
	// until the object has actually been removed.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(748329116)`); err != nil {
		return nil, err
	}
	var ipBytes, allBytes int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(size_bytes) FILTER (WHERE uploader_ip_hash=$1),0), COALESCE(SUM(size_bytes),0)
		FROM attachments WHERE owner_type='comment' AND owner_id IS NULL AND uploader_type='public-comment'`, a.UploaderIPHash).Scan(&ipBytes, &allBytes)
	if err != nil {
		return nil, err
	}
	if a.SizeBytes > ipLimit-ipBytes || a.SizeBytes > globalLimit-allBytes {
		return nil, domain.ErrUploadQuota
	}
	attachment, err := insertAttachment(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return attachment, nil
}

func (p *Postgres) CompleteAttachmentUpload(ctx context.Context, id string) error {
	result, err := p.pool.Exec(ctx, `UPDATE attachments SET upload_state='ready'
		WHERE id=$1 AND upload_state='uploading' AND owner_id IS NULL AND pending_until>NOW()`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return domain.ErrInvalidAttachment
	}
	return nil
}

func (p *Postgres) AbortAttachmentUpload(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE attachments SET upload_state='deleting',pending_until=NOW()
		WHERE id=$1 AND upload_state='uploading' AND owner_id IS NULL`, id)
	return err
}

func (p *Postgres) ClaimExpiredAttachments(ctx context.Context, limit int) ([]domain.Attachment, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := p.pool.Query(ctx, `WITH expired AS (
		SELECT id FROM attachments WHERE owner_id IS NULL AND pending_until<=NOW()
		  AND NOT EXISTS (SELECT 1 FROM attachment_post_refs r WHERE r.attachment_id=attachments.id)
		ORDER BY pending_until FOR UPDATE SKIP LOCKED LIMIT $1
	) UPDATE attachments a SET upload_state='deleting', pending_until=NOW()+INTERVAL '5 minutes'
	FROM expired WHERE a.id=expired.id
	RETURNING a.id,a.storage_provider,a.storage_key,a.delivery_mode`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(&a.ID, &a.StorageProvider, &a.StorageKey, &a.DeliveryMode); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteClaimedAttachment(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM attachments WHERE id=$1 AND owner_id IS NULL AND upload_state='deleting'`, id)
	return err
}
