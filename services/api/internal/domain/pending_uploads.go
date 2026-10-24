package domain

import (
	"context"
	"errors"
)

var ErrUploadQuota = errors.New("unclaimed upload quota exceeded")

type PendingUploadRepository interface {
	ReserveCommentAttachment(ctx context.Context, attachment Attachment, ipLimit, globalLimit int64) (*Attachment, error)
	CompleteAttachmentUpload(ctx context.Context, id string) error
	AbortAttachmentUpload(ctx context.Context, id string) error
	ClaimExpiredAttachments(ctx context.Context, limit int) ([]Attachment, error)
	DeleteClaimedAttachment(ctx context.Context, id string) error
}
