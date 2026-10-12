package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/chatwoot"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This fixture uses a real PostgreSQL database and the actual HTTP routes.
// Only Chatwoot, an external system, is replaced; it is not a Rails E2E test.
type orderReceiverStub struct {
	binding           string
	capabilityError   error
	loseFirstResponse bool
	sendCalls         int
	saved             map[string]*chatwoot.HandoffReceipt
	contents          map[string]string
}

func (c *orderReceiverStub) SendOrder(context.Context, string, string, string) error {
	return fmt.Errorf("legacy send must not be used for owned orders")
}
func (c *orderReceiverStub) HandoffCapability(context.Context, string) (*chatwoot.HandoffCapability, error) {
	if c.capabilityError != nil {
		return nil, c.capabilityError
	}
	return &chatwoot.HandoffCapability{Version: 1, BindingID: c.binding, IdempotentOrders: true}, nil
}
func (c *orderReceiverStub) SendOrderHandoff(_ context.Context, token, key, binding, content, referer string) (*chatwoot.HandoffReceipt, error) {
	c.sendCalls++
	if c.saved == nil {
		c.saved = map[string]*chatwoot.HandoffReceipt{}
		c.contents = map[string]string{}
	}
	if c.saved[key] == nil {
		c.saved[key] = &chatwoot.HandoffReceipt{Version: 1, BindingID: binding, ConversationID: 37, MessageID: int64(100 + len(c.saved)), RequestKey: key}
		c.contents[key] = content
	}
	if c.loseFirstResponse && c.sendCalls == 1 {
		return nil, &chatwoot.HandoffError{Code: "SUPPORT_UNREACHABLE", UnknownOutcome: true, Retryable: true}
	}
	return c.saved[key], nil
}

type handoffHTTPFixture struct {
	s         *Server
	product   *domain.Product
	affiliate *domain.Affiliate
	owner     *http.Cookie
	csrf      string
	key       string
	receiver  *orderReceiverStub
}

func newHandoffHTTPFixture(t *testing.T) *handoffHTTPFixture {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; real HTTP/PostgreSQL integration NOT RUN")
	}
	ctx := context.Background()
	repo, err := repository.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, err := repo.CreateAffiliate(ctx, fmt.Sprintf("QAHTTP%d", time.Now().UnixNano()), "unused-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.CreateProduct(ctx, domain.ProductInput{Title: "QA authoritative title", PriceCents: 3400, CommissionCents: 200, Currency: "CNY", Stock: 10, Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, sql := range []string{`DELETE FROM affiliate_order_support_deliveries WHERE order_id IN(SELECT id FROM affiliate_orders WHERE affiliate_id=$1)`, `DELETE FROM affiliate_order_requests WHERE order_id IN(SELECT id FROM affiliate_orders WHERE affiliate_id=$1)`, `DELETE FROM affiliate_orders WHERE affiliate_id=$1`, `DELETE FROM affiliates WHERE id=$1`} {
			if _, err := pool.Exec(ctx, sql, a.ID); err != nil {
				t.Errorf("clean QA HTTP fixture: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM products WHERE id=$1`, p.ID); err != nil {
			t.Errorf("clean QA HTTP product: %v", err)
		}
	})
	f := &handoffHTTPFixture{s: handoffTestServer(repo), product: p, affiliate: a, key: uuid.NewString(), receiver: &orderReceiverStub{binding: "verified-current-visitor"}}
	f.s.chatwoot = f.receiver
	first := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
	f.owner = first.Result().Cookies()[0]
	confirmed := f.request(http.MethodGet, "/api/orders/session", "", false)
	var session struct {
		Ready bool   `json:"ready"`
		CSRF  string `json:"csrfToken"`
	}
	if json.Unmarshal(confirmed.Body.Bytes(), &session) != nil || !session.Ready {
		t.Fatal("could not confirm QA owner")
	}
	f.csrf = session.CSRF
	return f
}

func (f *handoffHTTPFixture) request(method, path, body string, withWidget bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://shop.example"+path, strings.NewReader(body))
	r.AddCookie(f.owner)
	r.Header.Set("Origin", "https://shop.example")
	r.Header.Set("X-Order-CSRF", f.csrf)
	if method == http.MethodPost && path == "/api/orders" {
		r.Header.Set("Idempotency-Key", f.key)
	}
	if withWidget {
		r.AddCookie(&http.Cookie{Name: "cw_conversation", Value: "signed-test-widget-token"})
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

func (f *handoffHTTPFixture) create(t *testing.T) *domain.OrderHandoff {
	t.Helper()
	w := f.request(http.MethodPost, "/api/orders", fmt.Sprintf(`{"productSlug":%q,"recommenderWechatId":%q}`, f.product.Slug, f.affiliate.WechatID), false)
	if w.Code != http.StatusCreated {
		t.Fatalf("create HTTP order: %d %s", w.Code, w.Body.String())
	}
	var h domain.OrderHandoff
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Order.PriceCents != 3400 || h.Order.ProductTitle != "QA authoritative title" || h.Delivery.Status != "awaiting_session" || f.receiver.sendCalls != 0 {
		t.Fatalf("creation must preserve authoritative snapshot and wait for SDK: %+v", h)
	}
	return &h
}

func deliveryFromResponse(t *testing.T, w *httptest.ResponseRecorder) domain.OrderSupportDelivery {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("delivery response: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Delivery domain.OrderSupportDelivery `json:"supportDelivery"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Delivery
}

func TestOrderHandoffHTTPRecoverAndOwnerIsolation(t *testing.T) {
	f := newHandoffHTTPFixture(t)
	h := f.create(t)
	w := f.request(http.MethodPost, "/api/orders/recover", fmt.Sprintf(`{"requestKey":%q}`, f.key), false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), h.Order.ID) {
		t.Fatalf("recover unknown ID after response loss: %d %s", w.Code, w.Body.String())
	}
	replayed := f.request(http.MethodPost, "/api/orders", fmt.Sprintf(`{"productSlug":%q,"recommenderWechatId":%q}`, f.product.Slug, f.affiliate.WechatID), false)
	if replayed.Code != http.StatusOK || !strings.Contains(replayed.Body.String(), h.Order.ID) {
		t.Fatal("repeat POST did not recover original order")
	}
	conflict := f.request(http.MethodPost, "/api/orders", fmt.Sprintf(`{"productSlug":"different","recommenderWechatId":%q}`, f.affiliate.WechatID), false)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("changed same-key input: %s", conflict.Body.String())
	}
	original := f.owner
	first := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "https://shop.example/api/orders/session", nil))
	f.owner = first.Result().Cookies()[0]
	f.csrf = f.s.orderCSRF(f.owner.Value)
	for _, path := range []string{"/api/orders/recover", "/api/orders/" + h.Order.ID + "/support-delivery"} {
		body := fmt.Sprintf(`{"requestKey":%q}`, f.key)
		if strings.Contains(path, "support-delivery") {
			body = `{"expectedBindingId":"verified-current-visitor"}`
		}
		w := f.request(http.MethodPost, path, body, true)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "ORDER_NOT_FOUND") {
			t.Fatalf("cross-owner path exposed order %s: %d", path, w.Code)
		}
	}
	f.owner = original
	f.csrf = f.s.orderCSRF(f.owner.Value)
	unknown := f.request(http.MethodPost, "/api/orders/recover", fmt.Sprintf(`{"requestKey":%q}`, uuid.NewString()), false)
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Body.String(), "ORDER_NOT_FOUND") {
		t.Fatal("not-found recovery contract changed")
	}
}

func TestOrderHandoffHTTPUnknownReceiptRetriesOriginalOrder(t *testing.T) {
	f := newHandoffHTTPFixture(t)
	f.receiver.loseFirstResponse = true
	h := f.create(t)
	path := "/api/orders/" + h.Order.ID + "/support-delivery"
	body := `{"expectedBindingId":"verified-current-visitor"}`
	awaiting := deliveryFromResponse(t, f.request(http.MethodPost, path, body, false))
	if awaiting.Status != "awaiting_session" || f.receiver.sendCalls != 0 {
		t.Fatal("missing widget session sent a message")
	}
	first := deliveryFromResponse(t, f.request(http.MethodPost, path, body, true))
	if first.Status != "reconciling" || first.MessageID != 0 || first.Attempts != 1 || len(f.receiver.saved) != 1 {
		t.Fatalf("unknown result falsely reported sent: %+v", first)
	}
	second := deliveryFromResponse(t, f.request(http.MethodPost, path, body, true))
	if second.Status != "sent" || second.MessageID != 100 || second.ConversationID != 37 || second.Attempts != 2 || len(f.receiver.saved) != 1 {
		t.Fatalf("response loss did not safely reconcile same receipt: %+v", second)
	}
	third := deliveryFromResponse(t, f.request(http.MethodPost, path, body, true))
	if third.MessageID != second.MessageID || f.receiver.sendCalls != 2 {
		t.Fatal("sent order resent after confirmed receipt")
	}
	get := deliveryFromResponse(t, f.request(http.MethodGet, path, "", false))
	if get.MessageID != 100 {
		t.Fatal("receipt not persisted for GET")
	}
	if !strings.Contains(f.receiver.contents[h.Order.ID], h.Order.OrderCode) || !strings.Contains(f.receiver.contents[h.Order.ID], "QA authoritative title") {
		t.Fatal("message not built from stored snapshot")
	}
}

func TestOrderHandoffHTTPBindingAndCapabilityFailClosed(t *testing.T) {
	f := newHandoffHTTPFixture(t)
	h := f.create(t)
	path := "/api/orders/" + h.Order.ID + "/support-delivery"
	wrong := f.request(http.MethodPost, path, `{"expectedBindingId":"previous-visitor"}`, true)
	if wrong.Code != http.StatusConflict || !strings.Contains(wrong.Body.String(), "SUPPORT_BINDING_MISMATCH") || f.receiver.sendCalls != 0 {
		t.Fatal("SDK vs authenticated widget mismatch sent order")
	}
	f.receiver.capabilityError = &chatwoot.HandoffError{Code: "SUPPORT_CAPABILITY_UNAVAILABLE"}
	d := deliveryFromResponse(t, f.request(http.MethodPost, path, `{"expectedBindingId":"verified-current-visitor"}`, true))
	if d.Status != "action_required" || d.BindingID != "" || f.receiver.sendCalls != 0 {
		t.Fatal("missing receiver idempotency must block sends")
	}
	f.receiver.capabilityError = nil
	d = deliveryFromResponse(t, f.request(http.MethodPost, path, `{"expectedBindingId":"verified-current-visitor"}`, true))
	if d.Status != "sent" {
		t.Fatal("capability recovery did not send original order")
	}
	f.receiver.binding = "changed-visitor"
	wrong = f.request(http.MethodPost, path, `{"expectedBindingId":"changed-visitor"}`, true)
	if wrong.Code != http.StatusConflict || f.receiver.sendCalls != 1 {
		t.Fatal("persistent binding silently rebound")
	}
}

func TestOrderHandoffHTTPDistinctOrdersShareVisitorWithoutDedupingEachOther(t *testing.T) {
	f := newHandoffHTTPFixture(t)
	first := f.create(t)
	body := `{"expectedBindingId":"verified-current-visitor"}`
	firstReceipt := deliveryFromResponse(t, f.request(http.MethodPost, "/api/orders/"+first.Order.ID+"/support-delivery", body, true))
	f.key = uuid.NewString()
	// A new explicit operation does not reset the receiver's prior conversation.
	w := f.request(http.MethodPost, "/api/orders", fmt.Sprintf(`{"productSlug":%q,"recommenderWechatId":%q}`, f.product.Slug, f.affiliate.WechatID), false)
	if w.Code != http.StatusCreated {
		t.Fatalf("second operation: %d %s", w.Code, w.Body.String())
	}
	var second domain.OrderHandoff
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	secondReceipt := deliveryFromResponse(t, f.request(http.MethodPost, "/api/orders/"+second.Order.ID+"/support-delivery", body, true))
	if first.Order.ID == second.Order.ID || firstReceipt.MessageID == secondReceipt.MessageID || len(f.receiver.saved) != 2 || firstReceipt.ConversationID != secondReceipt.ConversationID {
		t.Fatal("distinct orders were lost or misrouted")
	}
}
