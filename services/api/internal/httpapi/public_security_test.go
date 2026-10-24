package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
)

type publicVerifierFunc func(context.Context, string, string, string) error

func (f publicVerifierFunc) Verify(ctx context.Context, token, ip, action string) error {
	return f(ctx, token, ip, action)
}

func TestBrowserWritesRequireOrigin(t *testing.T) {
	for _, path := range []string{"/api/admin/login", "/api/affiliate/access", "/api/orders", "/api/orders/recover", "/api/posts/test/comments", "/api/posts/test/comment-attachments"} {
		for _, origin := range []string{"", "null", "https://evil.example", "https://public.example"} {
			t.Run(path+"/"+origin, func(t *testing.T) {
				called := false
				h := adminOriginProtection(map[string]bool{"https://public.example": true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"content":"cross-site"}`))
				r.Header.Set("Origin", origin)
				r.Header.Set("Content-Type", "text/plain")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				allowed := origin == "https://public.example"
				if called != allowed {
					t.Fatalf("handler reached=%v, status=%d", called, w.Code)
				}
				if !allowed && w.Code != 403 {
					t.Fatalf("status=%d", w.Code)
				}
			})
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/posts/test/comments", nil)
	r.Header.Set("Origin", "https://public.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	adminOriginProtection(map[string]bool{"https://public.example": true})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("cross-site handler reached") })).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/api/integrations/wecom/callback", "/api/integrations/chatwoot/webhook", "/health"} {
		w := httptest.NewRecorder()
		adminOriginProtection(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != 204 {
			t.Fatalf("signed/internal route incorrectly blocked: %s", path)
		}
	}
}

func TestBrowserJSONRejectsSimpleContentTypes(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json; charset=utf-8"} {
		r := httptest.NewRequest(http.MethodPost, "/api/posts/test/comments", strings.NewReader(`{"content":"hello"}`))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		var input struct {
			Content string `json:"content"`
		}
		ok := decodeJSON(w, r, &input)
		if ok != (contentType == "application/json; charset=utf-8") {
			t.Fatalf("type=%q accepted=%v", contentType, ok)
		}
		if !ok && w.Code != 415 {
			t.Fatal(w.Code)
		}
	}
}

func TestPublicChallengesFailClosedAndUseServerAction(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		verifier    PublicChallengeVerifier
		status      int
	}{
		{"missing-config", "token", nil, 503},
		{"missing-token", "", publicVerifierFunc(func(context.Context, string, string, string) error { t.Fatal("empty token forwarded"); return nil }), 403},
		{"rejected", "token", publicVerifierFunc(func(context.Context, string, string, string) error { return ErrChallengeRejected }), 403},
		{"unavailable", "token", publicVerifierFunc(func(context.Context, string, string, string) error { return errors.New("secret provider error") }), 503},
		{"valid", "fresh", publicVerifierFunc(func(_ context.Context, token, ip, action string) error {
			if token != "fresh" || action != "comment_upload" {
				t.Fatal("wrong verification context")
			}
			return nil
		}), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{}, publicChallenge: tc.verifier}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/orders", nil)
			ok := s.requirePublicChallenge(w, r, tc.token, "comment_upload")
			if ok != (tc.status == 200) || w.Code != tc.status {
				t.Fatalf("ok=%v status=%d", ok, w.Code)
			}
			if strings.Contains(w.Body.String(), "secret provider") {
				t.Fatal("provider details leaked")
			}
		})
	}
	verifier, err := NewPublicChallenges(&config.Config{TurnstileSiteKey: "site", TurnstileSecretKey: strings.Repeat("s", 24), TurnstileExpectedHostname: "public.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifier.Verify(context.Background(), "token", "", "client-chosen-action"); !errors.Is(err, ErrChallengeUnavailable) {
		t.Fatal("unknown action accepted")
	}
}
