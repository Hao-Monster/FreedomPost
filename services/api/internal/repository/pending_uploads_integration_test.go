package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/google/uuid"
)

func pendingUploadFixture(t *testing.T) (*Postgres, *domain.Post) {
	t.Helper()
	repo := managedMediaFixture(t)
	return repo, mediaPost(t, repo, domain.VisibilityPublic)
}

func stagedComment(postID, ip string, bytes int64) domain.Attachment {
	expires := time.Now().Add(time.Hour)
	name := uuid.NewString() + ".txt"
	token := uuid.NewString()
	return domain.Attachment{OwnerType: "comment", UploaderType: "public-comment", UploaderIPHash: ip, ContextPostID: &postID,
		DeliveryMode: "managed", UploadState: "uploading", PendingUntil: &expires, ClaimToken: token, ClaimTokenHash: security.HashToken(token),
		OriginalFilename: "qa.txt", StoredFilename: name, StorageKey: name, StorageProvider: "local", MimeType: "text/plain", DetectedMime: "text/plain", SizeBytes: bytes}
}

func TestPendingUploadQuotaSerializesConcurrentReservations(t *testing.T) {
	repo, post := pendingUploadFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.ReserveCommentAttachment(ctx, stagedComment(post.ID, post.ID+string(rune('a'+i%2)), 10), 20, 30)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, domain.ErrUploadQuota) {
			t.Fatal(err)
		}
	}
	if success != 3 {
		t.Fatalf("accepted %d reservations, want 3", success)
	}
	var maxIP, total int64
	if err := repo.pool.QueryRow(ctx, `SELECT COALESCE(MAX(bytes),0),COALESCE(SUM(bytes),0) FROM (SELECT SUM(size_bytes) bytes FROM attachments WHERE context_post_id=$1 GROUP BY uploader_ip_hash) q`, post.ID).Scan(&maxIP, &total); err != nil {
		t.Fatal(err)
	}
	if maxIP > 20 || total > 30 {
		t.Fatalf("quota exceeded: ip=%d total=%d", maxIP, total)
	}
}

func TestCommentClaimContextExpiryAndResponseURL(t *testing.T) {
	repo, post := pendingUploadFixture(t)
	ctx := context.Background()
	a, err := repo.ReserveCommentAttachment(ctx, stagedComment(post.ID, post.ID, 10), 100, 1000)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.CreateCommentInput{PostSlug: post.Slug, Username: "QA", Content: "attachment", IPHash: "qa-network", Attachments: []domain.PendingAttachment{{ID: a.ID, ClaimToken: a.ClaimToken}}}
	if _, err = repo.CreateComment(ctx, input); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("uploading file claimed: %v", err)
	}
	if err = repo.CompleteAttachmentUpload(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreatePost(ctx, domain.CreatePostInput{Title: "QA other " + uuid.NewString(), Visibility: domain.VisibilityPublic, Currency: "CNY"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.pool.Exec(ctx, `DELETE FROM posts WHERE id=$1`, other.ID)
	wrong := input
	wrong.PostSlug = other.Slug
	if _, err = repo.CreateComment(ctx, wrong); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("cross-article claim: %v", err)
	}
	comment, err := repo.CreateComment(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ManagedMediaURL(post.ID, a.ID)
	if len(comment.Attachments) != 1 || comment.Attachments[0].URL != want {
		t.Fatalf("create response URL: %+v", comment.Attachments)
	}
	listed, err := repo.ListComments(ctx, post.Slug)
	if err != nil || len(listed) != 1 || listed[0].Attachments[0].URL != want {
		t.Fatalf("list response differs: %+v %v", listed, err)
	}
	if _, err = repo.CreateComment(ctx, input); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("reused claim: %v", err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-INTERVAL '1 hour' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	deleting, err := repo.ClaimExpiredAttachments(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range deleting {
		if item.ID == a.ID {
			t.Fatal("claimed attachment scheduled for deletion")
		}
	}
	stale, err := repo.ReserveCommentAttachment(ctx, stagedComment(post.ID, post.ID, 10), 100, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteAttachmentUpload(ctx, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-INTERVAL '1 hour' WHERE id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	input.Attachments[0].ID = stale.ID
	input.Attachments[0].ClaimToken = stale.ClaimToken
	if _, err = repo.CreateComment(ctx, input); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("expired claim: %v", err)
	}
}

func TestCleanupLeaseDoesNotStarveNewerExpiredObjects(t *testing.T) {
	repo, post := pendingUploadFixture(t)
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		a := stagedComment(post.ID, post.ID, 1)
		expired := time.Now().Add(-time.Hour)
		a.PendingUntil = &expired
		a.UploadState = "ready"
		if _, err := repo.CreateAttachment(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil || len(first) != 100 {
		t.Fatalf("first=%d err=%v", len(first), err)
	}
	second, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil || len(second) != 1 {
		t.Fatalf("failed deletions starved remaining work: second=%d err=%v", len(second), err)
	}
	if err := repo.CompleteAttachmentUpload(ctx, second[0].ID); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatal("deleting attachment revived")
	}
	if err := repo.DeleteClaimedAttachment(ctx, second[0].ID); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE id=$1)`, second[0].ID).Scan(&exists); err != nil || exists {
		t.Fatalf("metadata not retired: %v", err)
	}
}
