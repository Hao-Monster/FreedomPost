package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/google/uuid"
)

type uploadRepository struct {
	domain.Repository
	post       domain.Post
	attachment *domain.Attachment
	events     []string
	quota      bool
}

func (p *uploadRepository) GetPostBySlug(context.Context, string) (*domain.Post, error) {
	return &p.post, nil
}
func (p *uploadRepository) ReserveCommentAttachment(_ context.Context, a domain.Attachment, ip, global int64) (*domain.Attachment, error) {
	p.events = append(p.events, "reserve")
	if p.quota {
		return nil, domain.ErrUploadQuota
	}
	a.ID = uuid.NewString()
	p.attachment = &a
	return &a, nil
}
func (p *uploadRepository) CompleteAttachmentUpload(context.Context, string) error {
	p.events = append(p.events, "complete")
	p.attachment.UploadState = "ready"
	return nil
}
func (p *uploadRepository) AbortAttachmentUpload(context.Context, string) error {
	p.events = append(p.events, "abort")
	p.attachment.UploadState = "deleting"
	return nil
}
func (p *uploadRepository) ClaimExpiredAttachments(context.Context, int) ([]domain.Attachment, error) {
	return []domain.Attachment{*p.attachment}, nil
}
func (p *uploadRepository) DeleteClaimedAttachment(context.Context, string) error {
	p.events = append(p.events, "deleted")
	p.attachment = nil
	return nil
}

type uploadStore struct {
	storage.ManagedAdapter
	repo                *uploadRepository
	failPut, failDelete bool
}

func (s *uploadStore) Put(ctx context.Context, in storage.PutInput) (*storage.StoredObject, error) {
	s.repo.events = append(s.repo.events, "put")
	if s.failPut {
		return nil, errors.New("storage unavailable")
	}
	return s.ManagedAdapter.Put(ctx, in)
}
func (s *uploadStore) Delete(ctx context.Context, key string) error {
	s.repo.events = append(s.repo.events, "delete")
	if s.failDelete {
		return errors.New("delete unavailable")
	}
	return s.ManagedAdapter.Delete(ctx, key)
}

func TestPublicCommentUploadReservesBeforeWritingAndKeepsCleanupHandle(t *testing.T) {
	for _, name := range []string{"missing-challenge", "quota", "success", "write-failure", "cleanup-failure"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			base, err := storage.NewLocalAdapter(root, "")
			if err != nil {
				t.Fatal(err)
			}
			repo := &uploadRepository{post: domain.Post{ID: uuid.NewString(), Slug: "qa-post", Visibility: domain.VisibilityPublic}, quota: name == "quota"}
			store := &uploadStore{ManagedAdapter: base, repo: repo, failPut: name == "write-failure" || name == "cleanup-failure", failDelete: name == "cleanup-failure"}
			s := handoffTestServer(repo)
			s.cfg.UploadMaxBytes = 25 << 20
			s.cfg.VisitorHashSalt = "qa-salt"
			s.cfg.StorageDriver = "local"
			s.SetManagedStorage(store, map[string]storage.ManagedAdapter{"local": store})
			s.SetPublicChallenges(publicVerifierFunc(func(context.Context, string, string, string) error { return nil }))
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			part, err := form.CreateFormFile("file", "qa.txt")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte("QA upload"))
			_ = form.Close()
			r := httptest.NewRequest(http.MethodPost, "https://shop.example/api/posts/qa-post/comment-attachments", &body)
			r.Header.Set("Content-Type", form.FormDataContentType())
			r.Header.Set("Origin", "https://shop.example")
			if name != "missing-challenge" {
				r.Header.Set("X-Turnstile-Token", "fresh")
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			switch name {
			case "missing-challenge":
				if w.Code != 403 || len(repo.events) != 0 {
					t.Fatalf("missing challenge: %d %v", w.Code, repo.events)
				}
			case "quota":
				if w.Code != 429 || !reflect.DeepEqual(repo.events, []string{"reserve"}) {
					t.Fatalf("quota wrote bytes: %d %v", w.Code, repo.events)
				}
			case "success":
				if w.Code != 201 || !reflect.DeepEqual(repo.events, []string{"reserve", "put", "complete"}) {
					t.Fatalf("upload: %d %s %v", w.Code, w.Body.String(), repo.events)
				}
				var response struct {
					File struct{ ID, URL, ClaimToken string }
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.File.URL != domain.ManagedMediaURL(repo.post.ID, response.File.ID) || response.File.ClaimToken == "" {
					t.Fatal("managed response missing route/claim")
				}
				if repo.attachment.PendingUntil == nil || repo.attachment.ContextPostID == nil || *repo.attachment.ContextPostID != repo.post.ID || repo.attachment.UploaderIPHash == "" {
					t.Fatal("missing expiry/context/quota identity")
				}
			case "write-failure":
				if w.Code != 500 || repo.attachment != nil {
					t.Fatalf("failed upload not released: %d %+v %v", w.Code, repo.attachment, repo.events)
				}
			case "cleanup-failure":
				if w.Code != 500 || repo.attachment == nil || repo.attachment.UploadState != "deleting" {
					t.Fatal("lost failed-delete cleanup handle")
				}
			}
			files, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if name != "success" && len(files) != 0 {
				t.Fatal("denied upload left stored bytes")
			}
		})
	}
}
