package repository

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediamigrate"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediarefs"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/renderer"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func legacyCommentCleanupFixture(t *testing.T, repo *Postgres) (*domain.Post, *domain.Attachment, storage.ManagedAdapter, storage.ManagedAdapter) {
	t.Helper()
	ctx := context.Background()
	source, err := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	target, err := storage.NewLocalAdapter(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("QA migrated comment cleanup"), OriginalName: "qa.txt"})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	comment, err := repo.CreateComment(ctx, domain.CreateCommentInput{PostSlug: post.Slug, Username: "QA", Content: "QA attachment", IPHash: "qa-network"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "comment", OwnerID: &comment.ID, UploaderType: "public-comment", UploaderIPHash: "qa-original-ip",
		OriginalFilename: "qa.txt", StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey,
		PublicURL: object.PublicURL, MimeType: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return post, attachment, source, target
}

func expireDeletedMedia(t *testing.T, repo *Postgres, postID, attachmentID string) {
	t.Helper()
	ctx := context.Background()
	if deleted, err := repo.DeletePost(ctx, postID); err != nil || !deleted {
		t.Fatalf("delete post: deleted=%v err=%v", deleted, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-INTERVAL '1 second' WHERE id=$1`, attachmentID); err != nil {
		t.Fatal(err)
	}
}

func TestMigratedCommentCleanupKeepsAuditAndReleasesQuota(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	post, attachment, source, target := legacyCommentCleanupFixture(t, repo)
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Copy(ctx, repo.pool, sources, target); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err != nil {
		t.Fatal(err)
	}
	expireDeletedMedia(t, repo, post.ID, attachment.ID)
	claimed, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil || len(claimed) != 1 || claimed[0].ID != attachment.ID {
		t.Fatalf("cleanup candidates: %+v err=%v", claimed, err)
	}
	if err := target.Delete(ctx, claimed[0].StorageKey); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteClaimedAttachment(ctx, attachment.ID); err != nil {
		t.Fatalf("remove metadata after deleting private bytes: %v", err)
	}
	if err := repo.DeleteClaimedAttachment(ctx, attachment.ID); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if remaining, err := repo.GetManagedAttachment(ctx, attachment.ID); err != nil || remaining != nil {
		t.Fatalf("attachment metadata remains: %+v %v", remaining, err)
	}
	var retained bool
	if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_media_legacy_sources WHERE attachment_id=$1 AND retired_at IS NOT NULL AND cache_purged_at IS NOT NULL)`, attachment.ID).Scan(&retained); err != nil || !retained {
		t.Fatalf("completed migration audit was lost: retained=%v err=%v", retained, err)
	}
	other := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := repo.ReserveCommentAttachment(ctx, stagedComment(other.ID, "qa-fresh-ip", 1), attachment.SizeBytes, attachment.SizeBytes); err != nil {
		t.Fatalf("deleted comment bytes still consume global quota: %v", err)
	}
	if _, err := mediaSave(repo, other, "[old attachment]("+attachment.PublicURL+")"); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("deleted attachment lost its old URL tombstone: %v", err)
	}
	if plan, err := mediamigrate.Audit(ctx, repo.pool); err != nil || plan.PendingRetirement != 0 || plan.PendingCachePurge != 0 {
		t.Fatalf("completed cleanup changed migration status: %+v %v", plan, err)
	}
}

func TestMigratedMediaCleanupWaitsForRetirementAndPurge(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "waiting-for-retirement"
		if retire {
			name = "waiting-for-cache-purge"
		}
		t.Run(name, func(t *testing.T) {
			repo := managedMediaFixture(t)
			ctx := context.Background()
			post, attachment, source, target := legacyCommentCleanupFixture(t, repo)
			sources := map[string]storage.ManagedAdapter{"local": source}
			if err := mediamigrate.Copy(ctx, repo.pool, sources, target); err != nil {
				t.Fatal(err)
			}
			if retire {
				if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
					t.Fatal(err)
				}
			}
			expireDeletedMedia(t, repo, post.ID, attachment.ID)
			claimed, err := repo.ClaimExpiredAttachments(ctx, 100)
			if err != nil || len(claimed) != 0 {
				t.Fatalf("incomplete migration scheduled private copy for deletion: %+v %v", claimed, err)
			}
			current, err := repo.GetManagedAttachment(ctx, attachment.ID)
			if err != nil || current == nil {
				t.Fatalf("private copy metadata lost: %+v %v", current, err)
			}
			object, err := target.Open(ctx, current.StorageKey, http.MethodGet, "")
			if err != nil {
				t.Fatalf("private copy unavailable before completing migration: %v", err)
			}
			object.Body.Close()
			// Even a stale cleanup request must not remove protected metadata.
			if _, err := repo.pool.Exec(ctx, `UPDATE attachments SET upload_state='deleting' WHERE id=$1`, attachment.ID); err != nil {
				t.Fatal(err)
			}
			if err := repo.DeleteClaimedAttachment(ctx, attachment.ID); err != nil {
				t.Fatal(err)
			}
			if current, err := repo.GetManagedAttachment(ctx, attachment.ID); err != nil || current == nil {
				t.Fatalf("stale cleanup removed unfinished migration: %+v %v", current, err)
			}
		})
	}
}

type cleanupRacingMigrationStore struct {
	storage.ManagedAdapter
	beforePut func() error
}

func (s cleanupRacingMigrationStore) Put(ctx context.Context, input storage.PutInput) (*storage.StoredObject, error) {
	if err := s.beforePut(); err != nil {
		return nil, err
	}
	return s.ManagedAdapter.Put(ctx, input)
}

func TestMediaMigrationDoesNotReviveAttachmentClaimedForCleanup(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	_, attachment, source, target := legacyCommentCleanupFixture(t, repo)
	racingTarget := cleanupRacingMigrationStore{ManagedAdapter: target, beforePut: func() error {
		_, err := repo.pool.Exec(ctx, `UPDATE attachments SET owner_id=NULL,pending_until=NOW()-INTERVAL '1 second' WHERE id=$1`, attachment.ID)
		if err != nil {
			return err
		}
		claimed, err := repo.ClaimExpiredAttachments(ctx, 100)
		if err == nil && len(claimed) != 1 {
			t.Fatalf("cleanup did not win race: %+v", claimed)
		}
		return err
	}}
	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, racingTarget); err == nil {
		t.Fatal("migration committed after cleanup had claimed the original")
	}
	current, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || current == nil || current.DeliveryMode != "legacy_public" || current.UploadState != "deleting" {
		t.Fatalf("migration revived cleanup candidate: %+v %v", current, err)
	}
	object, err := source.Open(ctx, attachment.StorageKey, http.MethodHead, "")
	if err != nil {
		t.Fatalf("failed migration deleted public original: %v", err)
	}
	if object.Body != nil {
		object.Body.Close()
	}
}

func TestRetireReportsMissingPrivateMetadata(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	_, attachment, source, target := legacyCommentCleanupFixture(t, repo)
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Copy(ctx, repo.pool, sources, target); err != nil {
		t.Fatal(err)
	}
	// Emulate an independently missing metadata row without deleting its audit.
	if _, err := repo.pool.Exec(ctx, `ALTER TABLE managed_media_legacy_sources DROP CONSTRAINT IF EXISTS managed_media_legacy_sources_attachment_id_fkey; DELETE FROM attachments WHERE id=$1`, attachment.ID); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err == nil {
		t.Fatal("retirement silently skipped an orphaned unfinished migration")
	}
	object, err := source.Open(ctx, attachment.StorageKey, http.MethodHead, "")
	if err != nil {
		t.Fatalf("original unavailable after rejected retirement: %v", err)
	}
	if object.Body != nil {
		object.Body.Close()
	}
}

func TestLegacyUnclaimedCommentAttachmentReferencedByArticleSurvivesCleanup(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	post := mediaPost(t, repo, domain.VisibilityPublic)

	stored := uuid.NewString() + ".png"
	publicURL := "/api/uploads/" + stored
	past := time.Now().Add(-time.Minute)
	att, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "comment", UploaderType: "public-comment", UploaderIPHash: "fp-legacy",
		OriginalFilename: "legacy-comment.png", StoredFilename: stored,
		StorageProvider: "local", StorageKey: "2026/10/19/" + stored, PublicURL: publicURL,
		MimeType: "image/png", DetectedMime: "image/png", SizeBytes: 12,
		DeliveryMode: "legacy_public", UploadState: "ready", PendingUntil: &past,
	})
	if err != nil {
		t.Fatal(err)
	}

	md := "![图片](" + publicURL + ")"
	html := `<p><img src="` + publicURL + `" alt="图片"></p>`
	if _, err := repo.pool.Exec(ctx, `UPDATE posts SET content_markdown=$2, content_html=$3 WHERE id=$1`, post.ID, md, html); err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range claimed {
		if item.ID == att.ID {
			t.Fatalf("ClaimExpiredAttachments claimed legacy attachment still referenced by article: %+v", item)
		}
	}

	if err := repo.DeleteClaimedAttachment(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetManagedAttachment(ctx, att.ID)
	if err != nil || after == nil {
		t.Fatalf("DeleteClaimedAttachment deleted legacy attachment still referenced by article: %+v %v", after, err)
	}
}

func TestEncodedLegacyReferenceSurvivesUpgradeAndCleanup(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, err := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("QA existing attachment"), OriginalName: "qa.txt"})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "comment", UploaderType: "public-comment", OriginalFilename: "qa.txt",
		StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey,
		PublicURL: object.PublicURL, MimeType: object.MimeType, SizeBytes: object.SizeBytes,
		SHA256: object.SHA256, DeliveryMode: "legacy_public", UploadState: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	alias := "/api/uploads/" + strings.Replace(object.StoredFilename, "-", "%2D", 1)
	if mediarefs.LegacyIdentity(alias) != mediarefs.LegacyIdentity(object.PublicURL) {
		t.Fatal("alias is not same object")
	}
	markdown := "[QA](" + alias + ")"
	rendered := renderer.Render(markdown).HTML
	if _, err = repo.pool.Exec(ctx, `UPDATE posts SET content_markdown=$2,content_html=$3 WHERE id=$1`, post.ID, markdown, rendered); err != nil {
		t.Fatal(err)
	}
	migration16, err := os.ReadFile("../../../../deploy/migrations/0016_managed_media.sql")
	if err != nil {
		t.Fatal(err)
	}
	insert := string(migration16)
	insert = insert[strings.Index(insert, "INSERT INTO attachment_post_refs"):]
	insert = insert[:strings.Index(insert, "ON CONFLICT DO NOTHING;")+len("ON CONFLICT DO NOTHING;")]
	if _, err = repo.pool.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	migration17, err := os.ReadFile("../../../../deploy/migrations/0017_comment_upload_limits.sql")
	if err != nil {
		t.Fatal(err)
	}
	update := string(migration17)
	update = update[strings.Index(update, "UPDATE attachments a SET"):strings.Index(update, "CREATE INDEX")]
	if _, err = repo.pool.Exec(ctx, update); err != nil {
		t.Fatal(err)
	}
	var refs int
	var expiry bool
	if err = repo.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM attachment_post_refs WHERE attachment_id=$1),pending_until IS NOT NULL FROM attachments WHERE id=$1`, attachment.ID).Scan(&refs, &expiry); err != nil {
		t.Fatal(err)
	}
	if refs != 1 || expiry {
		t.Fatalf("expected encoded URL reference backfilled (refs=1) and not expired (expiry=false), got refs=%d expiry=%v", refs, expiry)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-interval '1 second' WHERE id=$1`, attachment.ID); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == attachment.ID {
			t.Fatalf("referenced encoded URL was claimed for cleanup: %+v", candidate)
		}
	}
}

func TestLegacySaveSerializesAgainstExpiredAttachmentCleanup(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("QA save cleanup race"), OriginalName: "qa.txt"})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	past := time.Now().Add(-time.Minute)
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "comment", UploaderType: "public-comment", OriginalFilename: "qa.txt",
		StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey,
		PublicURL: object.PublicURL, MimeType: object.MimeType, SizeBytes: object.SizeBytes,
		DeliveryMode: "legacy_public", UploadState: "ready", PendingUntil: &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	tracer := &commentMediaDeletePauseTracer{
		match: func(sql string) bool {
			return strings.HasPrefix(sql, "DELETE FROM attachment_post_refs WHERE post_id=")
		},
		pid:     make(chan uint32, 1),
		reached: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	cfg := repo.pool.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	saveRepo := &Postgres{pool: pool}
	finished := make(chan error, 1)
	go func() {
		_, saveErr := mediaSave(saveRepo, post, "[QA]("+object.PublicURL+")")
		finished <- saveErr
	}()
	select {
	case <-tracer.reached:
	case <-ctx.Done():
		t.Fatal("save did not reach ref synchronization")
	}
	candidates, err := repo.ClaimExpiredAttachments(ctx, 100)
	close(tracer.proceed)
	saveErr := <-finished
	if err != nil {
		t.Fatal(err)
	}
	if saveErr != nil {
		return
	}
	for _, candidate := range candidates {
		if candidate.ID == attachment.ID {
			t.Fatalf("cleanup claimed attachment while save transaction held reference lock: %+v", candidate)
		}
	}
}

func TestCleanupCanRecoverNonReadyOrphans(t *testing.T) {
	for _, mode := range []string{"aborted_upload", "interrupted_upload", "retry_failed_delete"} {
		t.Run(mode, func(t *testing.T) {
			repo, post := pendingUploadFixture(t)
			ctx := context.Background()
			a, err := repo.ReserveCommentAttachment(ctx, stagedComment(post.ID, "qa-recovery", 10), 10, 10)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "aborted_upload":
				if err = repo.AbortAttachmentUpload(ctx, a.ID); err != nil {
					t.Fatal(err)
				}
			case "interrupted_upload":
				if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-interval '1 second' WHERE id=$1`, a.ID); err != nil {
					t.Fatal(err)
				}
			case "retry_failed_delete":
				if err = repo.CompleteAttachmentUpload(ctx, a.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-interval '1 second' WHERE id=$1`, a.ID); err != nil {
					t.Fatal(err)
				}
				initial, claimErr := repo.ClaimExpiredAttachments(ctx, 100)
				if claimErr != nil || len(initial) != 1 {
					t.Fatalf("first lease: %v %v", initial, claimErr)
				}
				if _, err = repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-interval '1 second' WHERE id=$1`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			candidates, err := repo.ClaimExpiredAttachments(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range candidates {
				if candidate.ID == a.ID {
					return
				}
			}
			t.Fatalf("expired orphan cannot be claimed: mode=%s candidates=%d", mode, len(candidates))
		})
	}
}

func TestRemovedLegacyReferenceCanBeReclaimed(t *testing.T) {
	repo, post := pendingUploadFixture(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	a := stagedComment(post.ID, "qa-legacy", 10)
	a.DeliveryMode = "legacy_public"
	a.UploadState = "ready"
	a.PendingUntil = &past
	a.PublicURL = "/api/uploads/" + a.StoredFilename
	attachment, err := repo.CreateAttachment(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := mediaSave(repo, post, "[QA attachment]("+a.PublicURL+")")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mediaSave(repo, saved, "Attachment removed"); err != nil {
		t.Fatal(err)
	}
	hasReference, err := repo.HasPostAttachmentReference(ctx, post.ID, attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == attachment.ID && !hasReference {
			return
		}
	}
	t.Fatalf("last reference removed but attachment was not reclaimed: referenced=%v candidates=%d", hasReference, len(candidates))
}
