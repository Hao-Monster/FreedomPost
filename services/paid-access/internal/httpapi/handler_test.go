package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/auth"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/domain"
)

type fakeVerifier struct{ actions []string }

func (verifier *fakeVerifier) Verify(_ context.Context, _, _, action string) error {
	verifier.actions = append(verifier.actions, action)
	return nil
}

type allowAll struct{}

func (allowAll) Allow(context.Context, string, int, time.Duration) (bool, error) { return true, nil }

type oncePerKeyLimiter struct{ seen map[string]bool }

func (limiter *oncePerKeyLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, error) {
	if limiter.seen[key] {
		return false, nil
	}
	limiter.seen[key] = true
	return true, nil
}

type fakeStore struct {
	account    domain.Account
	article    domain.Article
	articleErr error
	sessionErr error
	entitled   bool
	sessions   map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		article:  domain.Article{ID: "post-1", Slug: "paid-post", Title: "付费文章", Excerpt: "公开预览", ContentHTML: "<p>绝密正文</p>", Visibility: "paid", PriceCents: 990, Currency: "CNY", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		sessions: make(map[string]bool),
	}
}

func (store *fakeStore) CreateAccount(_ context.Context, login, normalized, hash string) (domain.Account, error) {
	store.account = domain.Account{ID: "reader-1", LoginName: login, NormalizedLogin: normalized, PasswordHash: hash, CredentialVersion: 1, Status: "active", CreatedAt: time.Now()}
	return store.account, nil
}
func (store *fakeStore) FindAccountByLogin(context.Context, string) (domain.Account, error) {
	return store.account, nil
}
func (store *fakeStore) CreateSession(_ context.Context, _ string, hash string, _ int, _ domain.SessionMetadata) error {
	store.sessions[hash] = true
	return nil
}
func (store *fakeStore) FindAccountBySession(_ context.Context, hash string) (domain.Account, error) {
	if store.sessionErr != nil {
		return domain.Account{}, store.sessionErr
	}
	if store.sessions[hash] {
		return store.account, nil
	}
	return domain.Account{}, domain.ErrNotFound
}

func TestAuthenticatedEndpointsDoNotHideSessionStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.sessionErr = errors.New("database unavailable")
	handler, err := New(Config{Enabled: true, Store: store, Turnstile: &fakeVerifier{}, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: strings.Repeat("i", 32)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		origin bool
	}{
		{name: "session", method: http.MethodGet, path: "/api/reader/session"},
		{name: "orders", method: http.MethodGet, path: "/api/reader/orders"},
		{name: "create order", method: http.MethodPost, path: "/api/reader/posts/paid-post/orders", origin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader("{}"))
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: strings.Repeat("s", 48)})
			if test.origin {
				request.Header.Set("Origin", "https://example.com")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
func (store *fakeStore) TouchSession(context.Context, string) error { return nil }
func (store *fakeStore) RevokeSession(_ context.Context, hash string) error {
	delete(store.sessions, hash)
	return nil
}
func (store *fakeStore) RevokeAllSessions(context.Context, string) error {
	store.sessions = make(map[string]bool)
	return nil
}
func (store *fakeStore) FindArticle(_ context.Context, slug string) (domain.Article, error) {
	if store.articleErr != nil {
		return domain.Article{}, store.articleErr
	}
	if slug == store.article.Slug {
		return store.article, nil
	}
	return domain.Article{}, domain.ErrNotFound
}

func TestInternalAccessCheckDoesNotHideArticleStoreFailure(t *testing.T) {
	secret := strings.Repeat("i", 32)
	store := newFakeStore()
	store.account = domain.Account{ID: "reader-1", Status: "active"}
	sessionToken := strings.Repeat("s", 48)
	store.sessions[auth.HashSessionToken(sessionToken)] = true
	store.articleErr = errors.New("database unavailable")
	handler, err := New(Config{Enabled: true, Store: store, Turnstile: &fakeVerifier{}, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: secret})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	body := []byte(`{"sessionToken":"` + sessionToken + `","postSlug":"paid-post"}`)
	path := "/internal/access/check"
	timestamp := fmt.Sprint(time.Now().Unix())
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("X-FreedomPost-Timestamp", timestamp)
	request.Header.Set("X-FreedomPost-Nonce", "article-store-error-nonce")
	request.Header.Set("X-FreedomPost-Signature", testSignature(secret, timestamp, "article-store-error-nonce", http.MethodPost, path, "", body))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
func (store *fakeStore) HasEntitlement(context.Context, string, string) (bool, error) {
	return store.entitled, nil
}
func (store *fakeStore) CreateOrder(context.Context, string, domain.Article) (domain.Order, bool, error) {
	return domain.Order{ID: "order-1", OrderCode: "FP123", Status: "pending"}, true, nil
}
func (store *fakeStore) ListAccountOrders(context.Context, string) ([]domain.Order, error) {
	return []domain.Order{}, nil
}
func (store *fakeStore) ListAdminOrders(context.Context) ([]domain.Order, error) {
	return []domain.Order{}, nil
}
func (store *fakeStore) UpdateOrderStatus(context.Context, string, string, string) (domain.Order, error) {
	return domain.Order{}, nil
}
func (store *fakeStore) ListAccounts(context.Context) ([]domain.Account, error) {
	return []domain.Account{store.account}, nil
}
func (store *fakeStore) ResetPassword(context.Context, string, string, string) error { return nil }
func (store *fakeStore) Close()                                                      {}

func TestRegisterCreatesPersistentSecureSession(t *testing.T) {
	store := newFakeStore()
	verifier := &fakeVerifier{}
	handler, err := New(Config{Enabled: true, Store: store, Turnstile: verifier, Limiter: allowAll{}, TurnstileSiteKey: "1x-test-key", CookieSecure: true, PublicOrigin: "https://example.com", InternalSecret: strings.Repeat("i", 32)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/reader/register", strings.NewReader(`{"loginName":"Reader@example.com","password":"password phrase","turnstileToken":"`+strings.Repeat("t", 32)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(verifier.actions) != 1 || verifier.actions[0] != "reader_register" {
		t.Fatalf("actions=%v", verifier.actions)
	}
	cookie := response.Header().Get("Set-Cookie")
	for _, attribute := range []string{"fp_reader_session=", "HttpOnly", "Secure", "SameSite=Lax", "Max-Age="} {
		if !strings.Contains(cookie, attribute) {
			t.Fatalf("cookie missing %s: %s", attribute, cookie)
		}
	}
}

func TestPaidArticleNeverLeaksBodyWithoutEntitlement(t *testing.T) {
	store := newFakeStore()
	store.article.Excerpt = "绝密正文自动生成的摘要"
	handler, err := New(Config{Enabled: true, Store: store, Turnstile: &fakeVerifier{}, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: strings.Repeat("i", 32)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/reader/posts/paid-post", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if strings.Contains(response.Body.String(), "绝密正文") {
		t.Fatalf("locked response leaked content: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "绝密正文自动生成的摘要") {
		t.Fatalf("locked response leaked excerpt: %s", response.Body.String())
	}
	var body struct {
		Access struct {
			Locked bool `json:"locked"`
		} `json:"access"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Access.Locked {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestInternalRequestRejectsReplayAndActorTampering(t *testing.T) {
	secret := strings.Repeat("i", 32)
	handler, err := New(Config{Enabled: true, Store: newFakeStore(), Turnstile: &fakeVerifier{}, Limiter: &oncePerKeyLimiter{seen: make(map[string]bool)}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: secret})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	timestamp := fmt.Sprint(time.Now().Unix())
	path := "/internal/article-orders"
	nonce := "one-time-nonce-value"
	actor := "admin-a"
	signature := testSignature(secret, timestamp, nonce, http.MethodGet, path, actor, nil)

	first := httptest.NewRequest(http.MethodGet, path, nil)
	first.Header.Set("X-FreedomPost-Timestamp", timestamp)
	first.Header.Set("X-FreedomPost-Nonce", nonce)
	first.Header.Set("X-FreedomPost-Admin", actor)
	first.Header.Set("X-FreedomPost-Signature", signature)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first signed request status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}

	replay := httptest.NewRequest(http.MethodGet, path, nil)
	replay.Header = first.Header.Clone()
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request status=%d, want 401", replayResponse.Code)
	}

	tampered := httptest.NewRequest(http.MethodGet, path, nil)
	tampered.Header = first.Header.Clone()
	tampered.Header.Set("X-FreedomPost-Nonce", "different-nonce")
	tampered.Header.Set("X-FreedomPost-Admin", "admin-b")
	tamperedResponse := httptest.NewRecorder()
	handler.ServeHTTP(tamperedResponse, tampered)
	if tamperedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("actor-tampered request status=%d, want 401", tamperedResponse.Code)
	}
}

func TestEntitledSessionReceivesBodyAndOrderRequiresSession(t *testing.T) {
	store := newFakeStore()
	verifier := &fakeVerifier{}
	handler, err := New(Config{Enabled: true, Store: store, Turnstile: verifier, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: strings.Repeat("i", 32)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	register := httptest.NewRequest(http.MethodPost, "/api/reader/register", strings.NewReader(`{"loginName":"reader","password":"password phrase","turnstileToken":"`+strings.Repeat("t", 32)+`"}`))
	register.Header.Set("Origin", "https://example.com")
	registerResponse := httptest.NewRecorder()
	handler.ServeHTTP(registerResponse, register)
	cookies := registerResponse.Result().Cookies()
	if registerResponse.Code != http.StatusCreated || len(cookies) != 1 {
		t.Fatalf("register status=%d cookies=%v", registerResponse.Code, cookies)
	}

	unauthenticatedOrder := httptest.NewRequest(http.MethodPost, "/api/reader/posts/paid-post/orders", strings.NewReader("{}"))
	unauthenticatedOrder.Header.Set("Origin", "https://example.com")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticatedOrder)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated order status=%d", unauthenticatedResponse.Code)
	}

	authenticatedOrder := httptest.NewRequest(http.MethodPost, "/api/reader/posts/paid-post/orders", strings.NewReader("{}"))
	authenticatedOrder.Header.Set("Origin", "https://example.com")
	authenticatedOrder.AddCookie(cookies[0])
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticatedOrder)
	if authenticatedResponse.Code != http.StatusCreated || !strings.Contains(authenticatedResponse.Body.String(), `"orderCode":"FP123"`) {
		t.Fatalf("authenticated order status=%d body=%s", authenticatedResponse.Code, authenticatedResponse.Body.String())
	}

	store.entitled = true
	articleRequest := httptest.NewRequest(http.MethodGet, "/api/reader/posts/paid-post", nil)
	articleRequest.AddCookie(cookies[0])
	articleResponse := httptest.NewRecorder()
	handler.ServeHTTP(articleResponse, articleRequest)
	if articleResponse.Code != http.StatusOK || !strings.Contains(articleResponse.Body.String(), "绝密正文") {
		t.Fatalf("article status=%d body=%s", articleResponse.Code, articleResponse.Body.String())
	}
	if !strings.Contains(articleResponse.Header().Get("Set-Cookie"), "Max-Age=") {
		t.Fatalf("session was not renewed: %s", articleResponse.Header().Get("Set-Cookie"))
	}
}

func TestInternalAdminEndpointRejectsUnsignedRequest(t *testing.T) {
	handler, err := New(Config{Enabled: true, Store: newFakeStore(), Turnstile: &fakeVerifier{}, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: strings.Repeat("i", 32)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/article-orders", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInternalAdminEndpointAcceptsBodyBoundSignature(t *testing.T) {
	secret := strings.Repeat("i", 32)
	handler, err := New(Config{Enabled: true, Store: newFakeStore(), Turnstile: &fakeVerifier{}, Limiter: allowAll{}, TurnstileSiteKey: "site-key", PublicOrigin: "https://example.com", InternalSecret: secret})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/article-orders", nil)
	timestamp := time.Now().Unix()
	request.Header.Set("X-FreedomPost-Timestamp", fmt.Sprint(timestamp))
	request.Header.Set("X-FreedomPost-Nonce", "list-orders-nonce-value")
	request.Header.Set("X-FreedomPost-Admin", "admin-test")
	request.Header.Set("X-FreedomPost-Signature", testSignature(secret, fmt.Sprint(timestamp), "list-orders-nonce-value", http.MethodGet, "/internal/article-orders", "admin-test", nil))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	body := []byte(`{"status":"completed"}`)
	path := "/internal/article-orders/order-1"
	update := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(string(body)))
	update.Header.Set("X-FreedomPost-Timestamp", fmt.Sprint(timestamp))
	update.Header.Set("X-FreedomPost-Nonce", "update-order-nonce-value")
	update.Header.Set("X-FreedomPost-Admin", "admin-test")
	update.Header.Set("X-FreedomPost-Signature", testSignature(secret, fmt.Sprint(timestamp), "update-order-nonce-value", http.MethodPatch, path, "admin-test", body))
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("signed update status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}
}

func testSignature(secret, timestamp, nonce, method, path, actor string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n" + method + "\n" + path + "\n" + actor + "\n" + hex.EncodeToString(bodyHash[:])))
	return hex.EncodeToString(mac.Sum(nil))
}
