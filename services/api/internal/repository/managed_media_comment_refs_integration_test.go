package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediamigrate"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func migratedCommentReferences(t *testing.T, repo *Postgres) (*domain.Post, *domain.Post, *domain.Attachment, *storage.LocalAdapter, *storage.LocalAdapter) {
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
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("QA migrated comment shared by articles")})
	if err != nil {
		t.Fatal(err)
	}
	a := mediaPost(t, repo, domain.VisibilityPublic)
	b := mediaPost(t, repo, domain.VisibilityPublic)
	comment, err := repo.CreateComment(ctx, domain.CreateCommentInput{PostSlug: a.Slug, Username: "QA", Content: "QA comment", IPHash: "qa-ip"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "comment", OwnerID: &comment.ID, UploaderType: "public-comment",
		OriginalFilename: "qa.txt", StoredFilename: object.StoredFilename,
		StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL,
		MimeType: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range []*domain.Post{a, b} {
		if _, err := mediaSave(repo, post, "[attachment]("+object.PublicURL+")"); err != nil {
			t.Fatal(err)
		}
	}
	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, target); err != nil {
		t.Fatal(err)
	}
	return a, b, attachment, source, target
}

func TestMigratedCommentReferencesStayEditableAndIndependent(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a, b, attachment, source, target := migratedCommentReferences(t, repo)
	for _, original := range []*domain.Post{a, b} {
		post, err := repo.GetPostByID(ctx, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		canonical := domain.ManagedMediaURL(post.ID, attachment.ID)
		if !strings.Contains(post.ContentMarkdown, canonical) || !strings.Contains(post.ContentHTML, canonical) {
			t.Fatalf("migration did not establish this article's URL: %+v", post)
		}
		if _, err := mediaSave(repo, post, post.ContentMarkdown); err != nil {
			t.Fatalf("unchanged migrated comment reference cannot be saved: %v", err)
		}
	}
	unrelated := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := mediaSave(repo, unrelated, "[attachment]("+domain.ManagedMediaURL(a.ID, attachment.ID)+")"); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("new article acquired a comment reference without an existing association: %v", err)
	}
	updatedB, err := repo.GetPostByID(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	updatedB.Visibility = domain.VisibilityPrivate
	if _, err := mediaSave(repo, updatedB, updatedB.ContentMarkdown); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("comment reference bypassed retirement/purge gate: %v", err)
	}
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err != nil {
		t.Fatal(err)
	}
	if _, err := mediaSave(repo, updatedB, updatedB.ContentMarkdown); err != nil {
		t.Fatalf("fully migrated comment reference cannot become private: %v", err)
	}
	if _, err := mediaSave(repo, a, "article without a body attachment"); err != nil {
		t.Fatal(err)
	}
	for _, postID := range []string{a.ID, b.ID} {
		if found, err := repo.HasPostAttachmentReference(ctx, postID, attachment.ID); err != nil || !found {
			t.Fatalf("body removal affected surviving comment or other article: %s %v %v", postID, found, err)
		}
	}
}

func TestArticleSaveRejectsUncommittedCommentReferences(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	post := mediaPost(t, repo, domain.VisibilityPublic)
	ownerID := uuid.NewString()
	future := time.Now().Add(time.Hour)
	for _, test := range []struct {
		name, state string
		owner       *string
		pending     *time.Time
	}{
		{"uploading", "uploading", &ownerID, nil},
		{"deleting", "deleting", &ownerID, nil},
		{"unclaimed", "ready", nil, nil},
		{"staged", "ready", nil, &future},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := uuid.NewString()
			attachment, err := repo.CreateAttachment(ctx, domain.Attachment{
				OwnerType: "comment", OwnerID: test.owner, ContextPostID: &post.ID,
				UploaderType: "public-comment", DeliveryMode: "managed", UploadState: test.state,
				OriginalFilename: "qa.txt", StoredFilename: uuid.NewString() + ".txt",
				StorageProvider: "local", StorageKey: "qa", MimeType: "text/plain", SizeBytes: 2,
				PendingUntil: test.pending, ClaimTokenHash: security.HashToken(token),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.pool.Exec(ctx, `INSERT INTO attachment_post_refs(attachment_id,post_id) VALUES($1,$2)`, attachment.ID, post.ID); err != nil {
				t.Fatal(err)
			}
			_, err = mediaSave(repo, post, "[attachment]("+domain.ManagedMediaURL(post.ID, attachment.ID)+")", domain.PendingPostImage{ID: attachment.ID, ClaimToken: token})
			if !errors.Is(err, domain.ErrInvalidAttachment) {
				t.Fatalf("uncommitted comment attachment was accepted by post claim: %v", err)
			}
		})
	}
}

func TestDeletingCommentSourcePreservesSurvivingArticle(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a, b, attachment, source, target := migratedCommentReferences(t, repo)
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.DeletePost(ctx, a.ID); err != nil || !deleted {
		t.Fatalf("delete source article: %v %v", deleted, err)
	}
	surviving, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || surviving == nil || surviving.OwnerType != "post" || surviving.OwnerID == nil || *surviving.OwnerID != b.ID || surviving.PendingUntil != nil {
		t.Fatalf("comment media ownership was not transferred: %+v %v", surviving, err)
	}
	if found, err := repo.HasPostAttachmentReference(ctx, b.ID, attachment.ID); err != nil || !found {
		t.Fatalf("surviving article lost authorization: %v %v", found, err)
	}
	if _, err := target.Open(ctx, surviving.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatalf("surviving article lost its private object: %v", err)
	}
	postB, err := repo.GetPostByID(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mediaSave(repo, postB, postB.ContentMarkdown); err != nil {
		t.Fatalf("surviving article cannot save: %v", err)
	}
	if deleted, err := repo.DeletePost(ctx, b.ID); err != nil || !deleted {
		t.Fatalf("delete final article: %v %v", deleted, err)
	}
	released, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || released == nil || released.OwnerID != nil || released.PendingUntil == nil || !released.PendingUntil.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("final article deletion did not schedule cleanup: %+v %v", released, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE attachments SET pending_until=NOW()-INTERVAL '1 second' WHERE id=$1`, attachment.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil || len(claimed) != 1 || claimed[0].ID != attachment.ID {
		t.Fatalf("released media could not be reclaimed: %+v %v", claimed, err)
	}
	if err := target.Delete(ctx, claimed[0].StorageKey); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteClaimedAttachment(ctx, attachment.ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.DeletePost(ctx, b.ID); err != nil || deleted {
		t.Fatalf("repeated deletion must return false: %v %v", deleted, err)
	}
}

func TestDirectAttachmentDeletionRetainsIncompleteMigration(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a, _, attachment, source, target := migratedCommentReferences(t, repo)
	if err := repo.DeleteAttachmentsByIDs(ctx, []string{attachment.ID}); err != nil {
		t.Fatal(err)
	}
	if retained, err := repo.GetManagedAttachment(ctx, attachment.ID); err != nil || retained == nil {
		t.Fatalf("direct deletion bypassed unfinished migration protection: %+v %v", retained, err)
	}
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAttachmentsByIDs(ctx, []string{attachment.ID}); err != nil {
		t.Fatal(err)
	}
	if retained, err := repo.GetManagedAttachment(ctx, attachment.ID); err != nil || retained == nil {
		t.Fatalf("direct deletion bypassed unfinished purge protection: %+v %v", retained, err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAttachmentsByIDs(ctx, []string{attachment.ID}); err != nil {
		t.Fatal(err)
	}
	if removed, err := repo.GetManagedAttachment(ctx, attachment.ID); err != nil || removed != nil {
		t.Fatalf("completed migration attachment was not removed: %+v %v", removed, err)
	}
	var retained bool
	if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_media_legacy_sources WHERE attachment_id=$1)`, attachment.ID).Scan(&retained); err != nil || !retained {
		t.Fatalf("direct deletion lost the legacy URL tombstone: %v %v", retained, err)
	}
	if _, err := mediaSave(repo, a, "[old attachment]("+attachment.PublicURL+")"); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("deleted attachment's old public URL was accepted again: %v", err)
	}
}

func TestDeletePostIncludesCommentCommittedWhileWaiting(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	post := mediaPost(t, repo, domain.VisibilityPublic)
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var postID string
	if err := tx.QueryRow(ctx, `SELECT id FROM posts WHERE id=$1 FOR UPDATE`, post.ID).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	type deletion struct {
		deleted bool
		err     error
	}
	result := make(chan deletion, 1)
	go func() {
		deleted, err := repo.DeletePost(ctx, post.ID)
		result <- deletion{deleted, err}
	}()
	for {
		var waiting bool
		if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("deletion did not wait for the comment transaction")
		case <-time.After(10 * time.Millisecond):
		}
	}
	commentID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO comments(id,post_id,path,username,ip_hash,content) VALUES($1,$2,'0001','QA','qa-ip','QA')`, commentID, post.ID); err != nil {
		t.Fatal(err)
	}
	attachment, err := insertAttachment(ctx, tx, domain.Attachment{
		OwnerType: "comment", OwnerID: &commentID, ContextPostID: &post.ID,
		UploaderType: "public-comment", DeliveryMode: "managed", UploadState: "ready",
		OriginalFilename: "qa.txt", StoredFilename: uuid.NewString() + ".txt",
		StorageProvider: "local", StorageKey: "qa", MimeType: "text/plain", SizeBytes: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	finished := <-result
	if finished.err != nil || !finished.deleted {
		t.Fatalf("waiting deletion failed: %+v", finished)
	}
	remaining, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || remaining == nil || remaining.OwnerID != nil || remaining.PendingUntil == nil {
		t.Fatalf("concurrently committed comment media escaped cleanup: %+v %v", remaining, err)
	}
}

type commentMediaDeletePauseTracer struct {
	match   func(string) bool
	pid     chan uint32
	reached chan struct{}
	proceed chan struct{}
}

func (tracer *commentMediaDeletePauseTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	select {
	case tracer.pid <- conn.PgConn().PID():
	default:
	}
	if tracer.match(data.SQL) {
		close(tracer.reached)
		select {
		case <-tracer.proceed:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*commentMediaDeletePauseTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestConcurrentCommentSourceAndTargetDeletionReclaimsMedia(t *testing.T) {
	repo := managedMediaFixture(t)
	a, b, attachment, _, _ := migratedCommentReferences(t, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	newTracer := func(match func(string) bool) *commentMediaDeletePauseTracer {
		return &commentMediaDeletePauseTracer{match: match, pid: make(chan uint32, 1), reached: make(chan struct{}), proceed: make(chan struct{})}
	}
	tracerA := newTracer(func(sql string) bool { return strings.Contains(sql, "SET owner_type = 'post'") })
	tracerB := newTracer(func(sql string) bool { return sql == "commit" })
	newRepo := func(tracer *commentMediaDeletePauseTracer) *Postgres {
		cfg := repo.pool.Config()
		cfg.ConnConfig.Tracer = tracer
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return &Postgres{pool: pool}
	}
	ra, rb := newRepo(tracerA), newRepo(tracerB)
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { _, err := ra.DeletePost(ctx, a.ID); doneA <- err }()
	select {
	case <-tracerA.reached:
	case <-ctx.Done():
		t.Fatal("source deletion did not reach ownership transfer")
	}
	aPID := <-tracerA.pid
	go func() { _, err := rb.DeletePost(ctx, b.ID); doneB <- err }()
	var bPID uint32
	select {
	case bPID = <-tracerB.pid:
	case <-ctx.Done():
		t.Fatal("target deletion did not start")
	}
	// Before the fix B could reach COMMIT without observing A's ownership
	// transfer. With the fix B must wait on A's attachment lock first.
	for {
		select {
		case <-tracerB.reached:
			goto releaseSource
		case err := <-doneB:
			t.Fatalf("target deletion exited before synchronization: %v", err)
		default:
		}
		var blocked bool
		if err := repo.pool.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, aPID, bPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("target deletion neither waited on media nor reached commit")
		case <-time.After(10 * time.Millisecond):
		}
	}
releaseSource:
	close(tracerA.proceed)
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	close(tracerB.proceed)
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	remaining, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || remaining == nil || remaining.OwnerID != nil || remaining.PendingUntil == nil {
		t.Fatalf("both articles deleted but shared media escaped cleanup: %+v %v", remaining, err)
	}
	var references int
	if err := repo.pool.QueryRow(ctx, `SELECT COUNT(*) FROM attachment_post_refs WHERE attachment_id=$1`, attachment.ID).Scan(&references); err != nil || references != 0 {
		t.Fatalf("deleted articles retained body references: %d %v", references, err)
	}
}
