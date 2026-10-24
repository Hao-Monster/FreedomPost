package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/ratelimit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

type handoffTestRepository struct {
	domain.Repository
	created   int
	lookup    *domain.OrderHandoff
	lookupErr error
}

func (r *handoffTestRepository) LookupOwnedAffiliateOrder(context.Context, domain.CreateOwnedOrderInput) (*domain.OrderHandoff, error) {
	if r.lookup != nil || r.lookupErr != nil {
		return r.lookup, r.lookupErr
	}
	return nil, domain.ErrNotFound
}

func (r *handoffTestRepository) CreateOwnedAffiliateOrder(_ context.Context, input domain.CreateOwnedOrderInput) (*domain.OrderHandoff, bool, error) {
	order, err := r.CreateAffiliateOrder(context.Background(), input.CreateOrderInput)
	return &domain.OrderHandoff{Order: order, Delivery: domain.OrderSupportDelivery{Status: "awaiting_session"}}, true, err
}

func (r *handoffTestRepository) CreateAffiliateOrder(context.Context, domain.CreateOrderInput) (*domain.AffiliateOrder, error) {
	r.created++
	return &domain.AffiliateOrder{ID: "fb1ca6a3-c77a-4e37-8f39-64c5d8c7c27b", OrderCode: "FP-TEST"}, nil
}

func handoffTestServer(repo domain.Repository) *Server {
	return New(&config.Config{PublicSiteURL: "https://shop.example", CookieSecret: strings.Repeat("test-secret-", 4), CookieSecure: true}, repo,
		nil, ratelimit.NewMemory(), nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOrderSessionRequiresCookieRoundTrip(t *testing.T) {
	s := handoffTestServer(&handoffTestRepository{})
	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"ready":false`) {
		t.Fatalf("first session must be unconfirmed: status=%d body=%s", first.Code, first.Body.String())
	}
	cookies := first.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-fp_order_owner" || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("owner must use protected host-only cookie")
	}
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil)
	request.AddCookie(cookies[0])
	s.Handler().ServeHTTP(second, request)
	var session struct {
		Ready bool   `json:"ready"`
		CSRF  string `json:"csrfToken"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &session); err != nil || !session.Ready || session.CSRF == "" {
		t.Fatalf("second session must confirm cookie: %s", second.Body.String())
	}
	if second.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("owner responses must not be cached")
	}
}

func TestOrderOwnerRejectsTamperedExpiredAndCrossSiteSessions(t *testing.T) {
	s := handoffTestServer(&handoffTestRepository{})
	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
	cookie := first.Result().Cookies()[0]
	parts := strings.Split(cookie.Value, ".")
	for _, test := range []struct{ name, value string }{
		{"tampered", cookie.Value + "x"},
		{"expired", parts[0] + ".1." + security.HMACSign(s.cfg.CookieSecret, "order-owner:v1:"+parts[0]+".1")},
		{"unsigned", strings.Repeat("x", 52) + ".9999999999.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/123/support-delivery", nil)
			r.AddCookie(&http.Cookie{Name: cookie.Name, Value: test.value})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("invalid owner accepted: %d", w.Code)
			}
		})
	}
	for _, test := range []struct{ origin, fetch string }{{"https://evil.example", ""}, {"", "cross-site"}} {
		r := httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.fetch)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Fatalf("cross-site session set cookie: %d", w.Code)
		}
	}
}

func TestOrderWritesRequireOriginAndCSRF(t *testing.T) {
	s := handoffTestServer(&handoffTestRepository{})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
	cookie := w.Result().Cookies()[0]
	for _, test := range []struct{ name, origin, csrf string }{
		{"missing csrf", "https://shop.example", ""},
		{"wrong csrf", "https://shop.example", "bad"},
		{"missing origin", "", s.orderCSRF(cookie.Value)},
		{"cross origin", "https://evil.example", s.orderCSRF(cookie.Value)},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, path := range []string{"/api/orders", "/api/orders/recover", "/api/orders/fb1ca6a3-c77a-4e37-8f39-64c5d8c7c27b/support-delivery"} {
				r := httptest.NewRequest(http.MethodPost, "https://shop.example"+path, strings.NewReader(`{}`))
				r.AddCookie(cookie)
				r.Header.Set("Origin", test.origin)
				r.Header.Set("X-Order-CSRF", test.csrf)
				r.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusForbidden || (!strings.Contains(w.Body.String(), `"ORDER_CSRF_INVALID"`) && !strings.Contains(w.Body.String(), `"ORIGIN_FORBIDDEN"`)) {
					t.Fatalf("%s accepted invalid CSRF: %d %s", path, w.Code, w.Body.String())
				}
			}
		})
	}
}

type affiliateChallengeFunc func(context.Context, string, string, string) error

func (f affiliateChallengeFunc) Verify(ctx context.Context, token, ip, action string) error {
	return f(ctx, token, ip, action)
}

func orderCreateRequest(t *testing.T, s *Server, owned bool, token string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"productSlug": "qa-product", "recommenderWechatId": "Tester123", "turnstileToken": token})
	r := httptest.NewRequest(http.MethodPost, "https://shop.example/api/orders", strings.NewReader(string(body)))
	r.Header.Set("Origin", "https://shop.example")
	r.Header.Set("Content-Type", "application/json")
	if owned {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
		cookie := w.Result().Cookies()[0]
		r.AddCookie(cookie)
		r.Header.Set("X-Order-CSRF", s.orderCSRF(cookie.Value))
		r.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
	}
	return r
}

func TestOrderChallengeGatesBothCreationRoutes(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, tc := range []struct {
			name, token string
			verifier    bool
			err         error
			want        int
		}{
			{"unconfigured", "qa-token", false, nil, 503}, {"missing", "", true, nil, 403},
			{"rejected", "qa-token", true, ErrChallengeRejected, 403}, {"unavailable", "qa-token", true, ErrChallengeUnavailable, 503},
			{"accepted", "qa-token", true, nil, 201},
		} {
			t.Run(fmt.Sprintf("owned=%v/%s", owned, tc.name), func(t *testing.T) {
				repo := &handoffTestRepository{}
				s := handoffTestServer(repo)
				if tc.verifier {
					s.publicChallenge = affiliateChallengeFunc(func(_ context.Context, token, ip, action string) error {
						if token != "qa-token" || action != "order_create" || ip == "" {
							t.Fatalf("challenge arguments: token=%q action=%q ip=%q", token, action, ip)
						}
						return tc.err
					})
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, orderCreateRequest(t, s, owned, tc.token))
				if w.Code != tc.want || (tc.want != 201 && repo.created != 0) {
					t.Fatalf("status=%d created=%d body=%s", w.Code, repo.created, w.Body.String())
				}
			})
		}
	}
}

func TestOwnedOrderReplayPrecedesChallengeAndCreationQuota(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		repo := &handoffTestRepository{lookup: &domain.OrderHandoff{Order: &domain.AffiliateOrder{ID: "original"}, Delivery: domain.OrderSupportDelivery{Status: "awaiting_session"}}}
		if conflict {
			repo.lookup = nil
			repo.lookupErr = domain.ErrIdempotencyConflict
		}
		s := handoffTestServer(repo)
		r := orderCreateRequest(t, s, true, "")
		for range 20 {
			_, _ = s.limiter.Allow(r.Context(), "affiliate-order:"+security.HashText(s.remoteIP(r)), 20, time.Hour)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		want := 200
		if conflict {
			want = 409
		}
		if w.Code != want || repo.created != 0 {
			t.Fatalf("replay status=%d created=%d body=%s", w.Code, repo.created, w.Body.String())
		}
	}
}

func TestIdempotentOrderCannotCreateBeforeOwnerConfirmation(t *testing.T) {
	repo := &handoffTestRepository{}
	s := handoffTestServer(repo)
	request := httptest.NewRequest(http.MethodPost, "https://shop.example/api/orders", strings.NewReader(`{"productSlug":"test","recommenderWechatId":"Tester123"}`))
	request.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
	request.Header.Set("Origin", "https://shop.example")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"ORDER_OWNER_REQUIRED"`) || repo.created != 0 {
		t.Fatalf("unconfirmed owner created order: status=%d created=%d body=%s", response.Code, repo.created, response.Body.String())
	}

	crossOriginLegacy := httptest.NewRequest(http.MethodPost, "https://shop.example/api/orders", strings.NewReader(`{"productSlug":"test","recommenderWechatId":"Tester123"}`))
	crossOriginLegacy.Header.Set("Origin", "https://evil.example")
	legacyResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(legacyResp, crossOriginLegacy)
	if legacyResp.Code != http.StatusForbidden || repo.created != 0 {
		t.Fatalf("cross-origin legacy order should be rejected: status=%d created=%d body=%s", legacyResp.Code, repo.created, legacyResp.Body.String())
	}
}
