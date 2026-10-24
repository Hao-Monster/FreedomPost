package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediamigrate"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/renderer"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func managedMediaFixture(t *testing.T) *Postgres {
	t.Helper()
	address := os.Getenv("TEST_DATABASE_URL")
	if address == "" {
		t.Skip("TEST_DATABASE_URL missing; PostgreSQL integration NOT RUN")
	}
	ctx := context.Background()
	control, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	schema := "qa_media_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := control.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		control.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(address)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := control.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		control.Close()
		if err != nil {
			t.Error(err)
		}
	})
	files, err := filepath.Glob("../../../../deploy/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("migration files not found", err)
	}
	for _, file := range files {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(file), err)
		}
	}
	return &Postgres{pool: pool}
}

func TestManagedSharedMediaConcurrentOppositeOrders(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a := mediaPost(t, repo, domain.VisibilityPublic)
	b := mediaPost(t, repo, domain.VisibilityPrivate)
	urls := []string{}
	for range 2 {
		attachment, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OwnerID: &a.ID, OriginalFilename: "qa.txt", StoredFilename: uuid.NewString() + ".txt", StorageProvider: "local", StorageKey: "qa", MimeType: "text/plain", SizeBytes: 2, DeliveryMode: "managed", ContextPostID: &a.ID})
		if err != nil {
			t.Fatal(err)
		}
		urls = append(urls, domain.ManagedMediaURL(a.ID, attachment.ID))
	}
	markdownA := "[附件: A](" + urls[0] + ")\n\n[附件: B](" + urls[1] + ")"
	markdownB := "[附件: B](" + urls[1] + ")\n\n[附件: A](" + urls[0] + ")"
	start := make(chan struct{})
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			post, markdown := a, markdownA
			if i%2 == 1 {
				post, markdown = b, markdownB
			}
			_, err := mediaSave(repo, post, markdown)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent shared media save failed: %v", err)
		}
	}
}

func mediaPost(t *testing.T, repo *Postgres, visibility domain.PostVisibility) *domain.Post {
	t.Helper()
	p, err := repo.CreatePost(context.Background(), domain.CreatePostInput{Title: "QA media", Visibility: visibility, Currency: "CNY"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func mediaSave(repo *Postgres, p *domain.Post, markdown string, claims ...domain.PendingPostImage) (*domain.Post, error) {
	r := renderer.Render(markdown)
	return repo.UpdatePost(context.Background(), domain.UpdatePostInput{ID: p.ID, Title: p.Title, Slug: p.Slug, Visibility: p.Visibility, Currency: "CNY", ContentMarkdown: markdown, ContentHTML: r.HTML, SearchText: r.SearchText, Excerpt: r.Excerpt, ImportedImages: claims})
}

func TestManagedMediaClaimsAreScopedAndReferencesAreIndependent(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a := mediaPost(t, repo, domain.VisibilityPublic)
	b := mediaPost(t, repo, domain.VisibilityPrivate)
	future := time.Now().Add(time.Hour)
	token := "qa-media-claim"
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OriginalFilename: "qa.txt", StoredFilename: uuid.NewString() + ".txt", StorageProvider: "local", StorageKey: "qa", MimeType: "text/plain", DetectedMime: "text/plain", SizeBytes: 2, DeliveryMode: "managed", ContextPostID: &a.ID, UploadState: "ready", PendingUntil: &future, ClaimTokenHash: security.HashToken(token)})
	if err != nil {
		t.Fatal(err)
	}
	url := domain.ManagedMediaURL(a.ID, attachment.ID)
	markdown := "[附件: QA](" + url + ")"
	claim := domain.PendingPostImage{ID: attachment.ID, ClaimToken: token}
	if _, err := mediaSave(repo, b, markdown, claim); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("cross-post claim accepted: %v", err)
	}
	if _, err := mediaSave(repo, a, markdown, domain.PendingPostImage{ID: attachment.ID, ClaimToken: "wrong"}); !errors.Is(err, domain.ErrInvalidAttachment) {
		t.Fatalf("bad token accepted: %v", err)
	}
	a, err = mediaSave(repo, a, markdown, claim)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil || stored.PendingUntil != nil || stored.OwnerID == nil {
		t.Fatalf("claim not committed: %+v %v", stored, err)
	}
	b, err = mediaSave(repo, b, markdown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.ContentMarkdown, domain.ManagedMediaURL(b.ID, attachment.ID)) || strings.Contains(b.ContentHTML, url) {
		t.Fatal("copied media was not scoped to target article")
	}
	for _, post := range []*domain.Post{a, b} {
		found, err := repo.HasPostAttachmentReference(ctx, post.ID, attachment.ID)
		if err != nil || !found {
			t.Fatalf("missing shared reference %s: %v", post.ID, err)
		}
	}
	if _, err := mediaSave(repo, b, "removed"); err != nil {
		t.Fatal(err)
	}
	found, err := repo.HasPostAttachmentReference(ctx, b.ID, attachment.ID)
	if err != nil || found {
		t.Fatal("removed reference survived")
	}
	found, err = repo.HasPostAttachmentReference(ctx, a.ID, attachment.ID)
	if err != nil || !found {
		t.Fatal("removal damaged another article")
	}
	a.Visibility = domain.VisibilityPrivate
	if _, err := mediaSave(repo, a, a.ContentMarkdown); err != nil {
		t.Fatalf("managed public -> private needs no move: %v", err)
	}
}

func TestLegacyMigrationCopiesBeforeExplicitRetirement(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, _ := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	target, _ := storage.NewLocalAdapter(t.TempDir(), "")
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("QA migration bytes"), OriginalName: "qa.txt"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OriginalFilename: "qa.txt", StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL, MimeType: object.MimeType, DetectedMime: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	a := mediaPost(t, repo, domain.VisibilityPublic)
	b := mediaPost(t, repo, domain.VisibilityPublic)
	markdown := "[附件: QA](" + object.PublicURL + "?download=1#qa)"
	for _, post := range []*domain.Post{a, b} {
		if _, err := mediaSave(repo, post, markdown); err != nil {
			t.Fatal(err)
		}
	}
	a.Visibility = domain.VisibilityPrivate
	if _, err := mediaSave(repo, a, markdown); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("legacy exposure was not gated: %v", err)
	}
	plan, err := mediamigrate.Audit(ctx, repo.pool)
	if err != nil || len(plan.Items) != 1 || len(plan.Items[0].PostIDs) != 2 {
		t.Fatalf("audit shared reference: %+v %v", plan, err)
	}
	if _, err := source.Open(ctx, object.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatal("audit mutated source")
	}
	sources := map[string]storage.ManagedAdapter{"local": source}
	if err := mediamigrate.Copy(ctx, repo.pool, sources, target); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Copy(ctx, repo.pool, sources, target); err != nil {
		t.Fatalf("copy is not idempotent: %v", err)
	}
	if _, err := source.Open(ctx, object.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatal("copy deleted old public bytes")
	}
	updated, err := repo.GetPostByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Visibility = domain.VisibilityPrivate
	if !strings.Contains(updated.ContentMarkdown, domain.ManagedMediaURL(a.ID, attachment.ID)) {
		t.Fatal("copy did not rewrite article")
	}
	if _, err := mediaSave(repo, updated, updated.ContentMarkdown); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("retirement/purge gate missing: %v", err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err == nil {
		t.Fatal("purge confirmed while public original exists")
	}
	if err := mediamigrate.Retire(ctx, repo.pool, sources, map[string]storage.ManagedAdapter{"local": target}); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.ConfirmPurge(ctx, repo.pool, sources); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Open(ctx, object.StorageKey, http.MethodHead, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit retirement did not remove original")
	}
	if _, err := mediaSave(repo, updated, updated.ContentMarkdown); err != nil {
		t.Fatalf("completed migration remains blocked: %v", err)
	}
	stored, err := repo.GetManagedAttachment(ctx, attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Open(ctx, stored.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatal("private copy missing")
	}
	plan, err = mediamigrate.Audit(ctx, repo.pool)
	if err != nil || plan.PendingRetirement != 0 || plan.PendingCachePurge != 0 {
		t.Fatalf("migration incomplete: %+v %v", plan, err)
	}
}

func TestLegacyMigrationRefusesProductSharedMedia(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, _ := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	target, _ := storage.NewLocalAdapter(t.TempDir(), "")
	object, _ := source.Put(ctx, storage.PutInput{Data: []byte("QA shared")})
	_, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OriginalFilename: "qa.txt", StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL, MimeType: object.MimeType, SizeBytes: object.SizeBytes})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := mediaSave(repo, post, "[附件: QA]("+object.PublicURL+")"); err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateProduct(ctx, domain.ProductInput{Title: fmt.Sprint("QA public cover"), CoverURL: object.PublicURL, Currency: "CNY", Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, target); err == nil {
		t.Fatal("product-shared origin silently retired")
	}
	if _, err := source.Open(ctx, object.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatal("blocked migration touched source")
	}
}

func TestProtectedMediaLegacyAliasesAndTombstones(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	post := mediaPost(t, repo, domain.VisibilityPrivate)
	for _, base := range []string{"/api/uploads", "https://cdn.example/uploads"} {
		name := uuid.NewString() + ".txt"
		raw := base + "/" + name
		attachment, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OriginalFilename: "qa.txt", StoredFilename: name, StorageProvider: "local", StorageKey: name, PublicURL: raw, MimeType: "text/plain", SizeBytes: 2})
		if err != nil {
			t.Fatal(err)
		}
		variants := []string{raw, raw + "?download=1", raw + "#download", base + "//" + name, base + "/x/../" + name, base + "/%" + fmt.Sprintf("%X", name[0]) + name[1:]}
		if strings.HasPrefix(base, "https:") {
			variants = append(variants, "https://CDN.example:443/uploads/"+name, "//CDN.example:443/uploads/"+name)
		} else {
			variants = append(variants, "https://READER.example:443/api/uploads/"+name)
		}
		for _, variant := range variants {
			if _, err := mediaSave(repo, post, "[附件: QA]("+variant+")"); !errors.Is(err, domain.ErrMediaMigrationRequired) {
				t.Errorf("legacy alias accepted %s: %v", variant, err)
			}
		}
		if _, err := repo.pool.Exec(ctx, `INSERT INTO managed_media_legacy_sources(attachment_id,storage_provider,storage_key,public_url,sha256) VALUES($1,'local',$2,$3,'qa')`, attachment.ID, name, raw); err != nil {
			t.Fatal(err)
		}
		post.Visibility = domain.VisibilityPublic
		for _, variant := range variants {
			if _, err := mediaSave(repo, post, "[附件: QA]("+variant+")"); !errors.Is(err, domain.ErrMediaMigrationRequired) {
				t.Errorf("obsolete alias accepted %s: %v", variant, err)
			}
		}
		post.Visibility = domain.VisibilityPrivate
	}
}

func TestMigrationCommitFailureRetainsVerifiedPrivateCopy(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	publicRoot := t.TempDir()
	privateRoot := t.TempDir()
	source, _ := storage.NewLocalAdapter(publicRoot, "/api/uploads")
	target, _ := storage.NewLocalAdapter(privateRoot, "")
	object, _ := source.Put(ctx, storage.PutInput{Data: []byte("QA commit failure bytes")})
	_, err := repo.CreateAttachment(ctx, domain.Attachment{OwnerType: "post", OriginalFilename: "qa.txt", StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL, MimeType: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := mediaSave(repo, post, "[附件: QA]("+object.PublicURL+")"); err != nil {
		t.Fatal(err)
	}
	_, err = repo.pool.Exec(ctx, `CREATE FUNCTION qa_reject_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit-stage failure'; END $$;
	 CREATE CONSTRAINT TRIGGER qa_commit_failure AFTER INSERT ON managed_media_legacy_sources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION qa_reject_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, target); err == nil {
		t.Fatal("commit failure not surfaced")
	}
	files, err := os.ReadDir(privateRoot)
	if err != nil || len(files) != 1 {
		t.Fatalf("verified private copy was deleted on commit error: %d %v", len(files), err)
	}
	if _, err := source.Open(ctx, object.StorageKey, http.MethodHead, ""); err != nil {
		t.Fatal("commit failure deleted original")
	}
}

func TestDeletePostReassignsSharedMediaAndSchedulesGraceCleanup(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	a := mediaPost(t, repo, domain.VisibilityPublic)
	b := mediaPost(t, repo, domain.VisibilityPublic)
	c := mediaPost(t, repo, domain.VisibilityPublic)

	shared, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OwnerID: &a.ID, ContextPostID: &a.ID,
		OriginalFilename: "shared.txt", StoredFilename: uuid.NewString() + ".txt",
		StorageProvider: "local", StorageKey: "shared", MimeType: "text/plain",
		SizeBytes: 6, DeliveryMode: "managed", UploadState: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	onlyA, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OwnerID: &a.ID, ContextPostID: &a.ID,
		OriginalFilename: "only-a.txt", StoredFilename: uuid.NewString() + ".txt",
		StorageProvider: "local", StorageKey: "only-a", MimeType: "text/plain",
		SizeBytes: 6, DeliveryMode: "managed", UploadState: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OwnerID: &c.ID, ContextPostID: &c.ID,
		OriginalFilename: "unrelated.txt", StoredFilename: uuid.NewString() + ".txt",
		StorageProvider: "local", StorageKey: "unrelated", MimeType: "text/plain",
		SizeBytes: 9, DeliveryMode: "managed", UploadState: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mediaSave(repo, a, "[附件: S]("+domain.ManagedMediaURL(a.ID, shared.ID)+")\n\n[附件: A]("+domain.ManagedMediaURL(a.ID, onlyA.ID)+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := mediaSave(repo, b, "[附件: S]("+domain.ManagedMediaURL(a.ID, shared.ID)+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := mediaSave(repo, c, "[附件: U]("+domain.ManagedMediaURL(c.ID, unrelated.ID)+")"); err != nil {
		t.Fatal(err)
	}

	deleted, err := repo.DeletePost(ctx, a.ID)
	if err != nil || !deleted {
		t.Fatalf("DeletePost(a) = %v, %v", deleted, err)
	}

	sharedAfterA, err := repo.GetManagedAttachment(ctx, shared.ID)
	if err != nil || sharedAfterA == nil || sharedAfterA.OwnerID == nil || *sharedAfterA.OwnerID != b.ID || sharedAfterA.PendingUntil != nil {
		t.Fatalf("shared attachment was not reassigned to post B: %+v %v", sharedAfterA, err)
	}
	onlyAAfterA, err := repo.GetManagedAttachment(ctx, onlyA.ID)
	if err != nil || onlyAAfterA == nil || onlyAAfterA.OwnerID != nil || onlyAAfterA.PendingUntil == nil || !onlyAAfterA.PendingUntil.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("unreferenced attachment did not receive 24h grace window: %+v %v", onlyAAfterA, err)
	}
	unrelatedAfterA, err := repo.GetManagedAttachment(ctx, unrelated.ID)
	if err != nil || unrelatedAfterA == nil || unrelatedAfterA.OwnerID == nil || *unrelatedAfterA.OwnerID != c.ID || unrelatedAfterA.PendingUntil != nil {
		t.Fatalf("unrelated post attachment was modified: %+v %v", unrelatedAfterA, err)
	}
}

func TestCreatePostClaimsStagedMedia(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	token := "qa-create-post-claim"
	staged, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OriginalFilename: "staged.txt", StoredFilename: uuid.NewString() + ".txt",
		StorageProvider: "local", StorageKey: "staged", MimeType: "text/plain", DetectedMime: "text/plain",
		SizeBytes: 6, DeliveryMode: "managed", UploadState: "ready", PendingUntil: &future,
		ClaimTokenHash: security.HashToken(token),
	})
	if err != nil {
		t.Fatal(err)
	}
	tempPostID := "00000000-0000-4000-8000-000000000099"
	markdown := "[附件: Staged](" + domain.ManagedMediaURL(tempPostID, staged.ID) + ")"
	rendered := renderer.Render(markdown)
	created, err := repo.CreatePost(ctx, domain.CreatePostInput{
		Title: "Create with claim", Visibility: domain.VisibilityPrivate, Currency: "CNY",
		ContentMarkdown: markdown, ContentHTML: rendered.HTML, SearchText: rendered.SearchText, Excerpt: rendered.Excerpt,
		ImportedImages: []domain.PendingPostImage{{ID: staged.ID, ClaimToken: token}},
	})
	if err != nil {
		t.Fatalf("CreatePost with staged media failed: %v", err)
	}
	if !strings.Contains(created.ContentMarkdown, domain.ManagedMediaURL(created.ID, staged.ID)) {
		t.Fatalf("CreatePost did not rewrite staged media URL to new post ID: %s", created.ContentMarkdown)
	}
	ref, err := repo.HasPostAttachmentReference(ctx, created.ID, staged.ID)
	if err != nil || !ref {
		t.Fatalf("CreatePost did not record attachment_post_refs: %v %v", ref, err)
	}
}

func TestLegacyMigrationSupportsHTMLOnlyPost(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, _ := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	target, _ := storage.NewLocalAdapter(t.TempDir(), "")
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("HTML only post bytes"), OriginalName: "legacy.png"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OriginalFilename: "legacy.png", StoredFilename: object.StoredFilename,
		StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL,
		MimeType: object.MimeType, DetectedMime: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := repo.pool.Exec(ctx, `UPDATE posts SET content_markdown='', content_html=$2 WHERE id=$1`, post.ID, `<p><img src="`+object.PublicURL+`" alt="legacy"></p>`); err != nil {
		t.Fatal(err)
	}
	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, target); err != nil {
		t.Fatalf("mediamigrate.Copy failed on HTML-only legacy post: %v", err)
	}
	updated, err := repo.GetPostByID(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated.ContentHTML, domain.ManagedMediaURL(post.ID, att.ID)) {
		t.Fatalf("HTML-only post was not rewritten to managed media URL: %s", updated.ContentHTML)
	}
}

func TestMigrationRemovesStaleOwnerReferenceWhenAttachmentMovedToPrivatePost(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, _ := storage.NewLocalAdapter(t.TempDir(), "/api/uploads")
	target, _ := storage.NewLocalAdapter(t.TempDir(), "")
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("moved to private B"), OriginalName: "moved.txt"})
	if err != nil {
		t.Fatal(err)
	}
	publicA := mediaPost(t, repo, domain.VisibilityPublic)
	privateB := mediaPost(t, repo, domain.VisibilityPrivate)
	att, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OwnerID: &publicA.ID, ContextPostID: &publicA.ID,
		OriginalFilename: "moved.txt", StoredFilename: object.StoredFilename,
		StorageProvider: "local", StorageKey: object.StorageKey, PublicURL: object.PublicURL,
		MimeType: object.MimeType, DetectedMime: object.MimeType, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
		DeliveryMode: "legacy_public", UploadState: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE posts SET content_markdown='No attachment anymore', content_html='<p>No attachment anymore</p>' WHERE id=$1`, publicA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE posts SET content_markdown=$2, content_html=$3 WHERE id=$1`, privateB.ID, "[附件: Moved]("+object.PublicURL+")", `<p><a href="`+object.PublicURL+`">Moved</a></p>`); err != nil {
		t.Fatal(err)
	}
	// Simulate a stale owner_id backfill row from before the article removed the attachment.
	if _, err := repo.pool.Exec(ctx, `INSERT INTO attachment_post_refs (attachment_id, post_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, att.ID, publicA.ID); err != nil {
		t.Fatal(err)
	}

	if err := mediamigrate.Copy(ctx, repo.pool, map[string]storage.ManagedAdapter{"local": source}, target); err != nil {
		t.Fatal(err)
	}
	hasA, err := repo.HasPostAttachmentReference(ctx, publicA.ID, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasB, err := repo.HasPostAttachmentReference(ctx, privateB.ID, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasA || !hasB {
		t.Fatalf("expected stale public ref removed and private ref kept: hasA=%v hasB=%v", hasA, hasB)
	}
}

func TestLegacyBrowserBackslashAliasProtectedAndAudited(t *testing.T) {
	repo := managedMediaFixture(t)
	ctx := context.Background()
	source, err := storage.NewLocalAdapter(t.TempDir(), "https://reader.example/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	object, err := source.Put(ctx, storage.PutInput{Data: []byte("legacy public secret"), OriginalName: "secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	post := mediaPost(t, repo, domain.VisibilityPublic)
	if _, err := repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "post", OwnerID: &post.ID, UploaderType: "admin", OriginalFilename: "secret.txt",
		StoredFilename: object.StoredFilename, StorageProvider: "local", StorageKey: object.StorageKey,
		PublicURL: object.PublicURL, MimeType: object.MimeType, DetectedMime: object.MimeType,
		SizeBytes: object.SizeBytes, SHA256: object.SHA256, DeliveryMode: "legacy_public", UploadState: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	browserAlias := strings.ReplaceAll(object.PublicURL, "/", `\`)
	saved, err := mediaSave(repo, post, `<a href="`+browserAlias+`">Download attachment</a>`)
	if err != nil {
		t.Fatal(err)
	}
	saved.Visibility = domain.VisibilityPrivate
	if _, err := mediaSave(repo, saved, saved.ContentMarkdown); !errors.Is(err, domain.ErrMediaMigrationRequired) {
		t.Fatalf("expected private save of browser backslash alias to fail with ErrMediaMigrationRequired, got %v", err)
	}

	plan, err := mediamigrate.Audit(ctx, repo.pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("expected Audit to find 1 item for browser backslash alias, got %+v", plan)
	}
}
