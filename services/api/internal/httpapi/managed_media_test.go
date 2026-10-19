package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/paidaccess"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

type mediaTestRepo struct {
	domain.Repository
	attachment *domain.Attachment
	post       *domain.Post
	referenced bool
}

func (r *mediaTestRepo) GetManagedAttachment(context.Context, string) (*domain.Attachment, error) {
	return r.attachment, nil
}
func (r *mediaTestRepo) GetPostByID(context.Context, string) (*domain.Post, error) {
	return r.post, nil
}
func (r *mediaTestRepo) HasPostAttachmentReference(context.Context, string, string) (bool, error) {
	return r.referenced, nil
}

type mediaTestStore struct {
	storage.ManagedAdapter
	opens int
}

func (s *mediaTestStore) Open(context.Context, string, string, string) (*storage.ObjectRead, error) {
	s.opens++
	return &storage.ObjectRead{Body: io.NopCloser(strings.NewReader("private bytes")), StatusCode: 200, ContentLength: 13}, nil
}

func TestManagedMediaChecksCurrentArticleBeforeOpeningStorage(t *testing.T) {
	postID := "00000000-0000-4000-8000-000000000001"
	attachmentID := "00000000-0000-4000-8000-000000000002"
	entitlement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]string
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input["sessionToken"] == "unavailable" {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": input["sessionToken"] == "purchased" && input["postSlug"] == "current-slug"})
	}))
	defer entitlement.Close()
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name, method string
		visibility   domain.PostVisibility
		cookie       string
		referenced   bool
		pending      *time.Time
		state        string
		status       int
	}{
		{"public", "GET", domain.VisibilityPublic, "", true, nil, "ready", 200},
		{"public head", "HEAD", domain.VisibilityPublic, "", true, nil, "ready", 200},
		{"private", "GET", domain.VisibilityPrivate, "", true, nil, "ready", 404},
		{"private range", "GET", domain.VisibilityPrivate, "purchased", true, nil, "ready", 404},
		{"paid anonymous head", "HEAD", domain.VisibilityPaid, "", true, nil, "ready", 404},
		{"paid wrong account", "GET", domain.VisibilityPaid, "unpaid", true, nil, "ready", 404},
		{"paid purchased", "GET", domain.VisibilityPaid, "purchased", true, nil, "ready", 200},
		{"paid service down", "GET", domain.VisibilityPaid, "unavailable", true, nil, "ready", 500},
		{"unrelated article", "GET", domain.VisibilityPublic, "", false, nil, "ready", 404},
		{"staged public comment", "GET", domain.VisibilityPublic, "", true, &future, "ready", 404},
		{"upload in progress", "GET", domain.VisibilityPublic, "", true, nil, "uploading", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &mediaTestStore{}
			repo := &mediaTestRepo{attachment: &domain.Attachment{ID: attachmentID, DeliveryMode: "managed", StorageProvider: "local", UploadState: tc.state, ContextPostID: &postID, PendingUntil: tc.pending, MimeType: "text/plain", OriginalFilename: "qa.txt"}, post: &domain.Post{ID: postID, Slug: "current-slug", Visibility: tc.visibility}, referenced: tc.referenced}
			s := &Server{repo: repo, managedStores: map[string]storage.ManagedAdapter{"local": store}, paidAccess: paidaccess.NewClient(entitlement.URL, "qa-signing"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			request := httptest.NewRequest(tc.method, domain.ManagedMediaURL(postID, attachmentID), nil)
			request.SetPathValue("postID", postID)
			request.SetPathValue("attachmentID", attachmentID)
			request.Header.Set("Range", "bytes=0-3")
			if tc.cookie != "" {
				request.AddCookie(&http.Cookie{Name: readerCookieName, Value: tc.cookie})
			}
			response := httptest.NewRecorder()
			s.serveManagedMedia(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != 200 && store.opens != 0 {
				t.Fatal("read private bytes before authorization")
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("managed media can be cached")
			}
			if tc.method == "HEAD" && response.Body.Len() != 0 && tc.status == 200 {
				t.Fatal("HEAD sent bytes")
			}
		})
	}
}

func TestManagedMediaAdminPreviewUsesRealSessionStore(t *testing.T) {
	postID := "00000000-0000-4000-8000-000000000001"
	attachmentID := "00000000-0000-4000-8000-000000000002"
	sessions := affiliateSessionFixture(t)
	if err := sessions.SetAdminSession(context.Background(), security.HashToken("qa-admin-session"), domain.AdminSession{Username: "qa-admin", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contextID string
		expiry          time.Time
		status          int
	}{
		{"active stage", postID, time.Now().Add(time.Hour), 200},
		{"cross article stage", "00000000-0000-4000-8000-000000000003", time.Now().Add(time.Hour), 404},
		{"expired stage", postID, time.Now().Add(-time.Hour), 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &mediaTestStore{}
			repo := &mediaTestRepo{attachment: &domain.Attachment{ID: attachmentID, DeliveryMode: "managed", StorageProvider: "local", UploadState: "ready", ContextPostID: &tc.contextID, PendingUntil: &tc.expiry, MimeType: "text/plain"}, post: &domain.Post{ID: postID, Visibility: domain.VisibilityPrivate}}
			s := &Server{repo: repo, sessions: sessions, managedStores: map[string]storage.ManagedAdapter{"local": store}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			r := httptest.NewRequest("GET", domain.ManagedMediaURL(postID, attachmentID), nil)
			r.SetPathValue("postID", postID)
			r.SetPathValue("attachmentID", attachmentID)
			r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "qa-admin-session"})
			w := httptest.NewRecorder()
			s.serveManagedMedia(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status != 200 && store.opens != 0 {
				t.Fatal("denied preview touched object")
			}
		})
	}
}
