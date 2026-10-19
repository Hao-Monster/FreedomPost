package domain

import (
	"context"
	"errors"
)

var ErrMediaMigrationRequired = errors.New("legacy media migration required")

func ManagedMediaURL(postID, attachmentID string) string {
	return "/api/post-media/" + postID + "/" + attachmentID
}

// ManagedMediaRepository resolves metadata; it never treats an object URL as
// evidence that a reader can access an article.
type ManagedMediaRepository interface {
	GetManagedAttachment(ctx context.Context, id string) (*Attachment, error)
	HasPostAttachmentReference(ctx context.Context, postID, attachmentID string) (bool, error)
}
