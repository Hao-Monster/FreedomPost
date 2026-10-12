package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const handoffOrderID = "5ac80dfe-eb09-40d4-9c4f-cb4a343ac03e"
const handoffBinding = "verified-visitor-binding"

func TestHandoffCapabilityRequiresVersionAndIdempotency(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"supported", `{"version":1,"binding_id":"verified-visitor-binding","idempotent_orders":true}`, 200, true},
		{"old server", `{}`, 200, false},
		{"wrong version", `{"version":2,"binding_id":"binding","idempotent_orders":true}`, 200, false},
		{"no idempotency", `{"version":1,"binding_id":"binding","idempotent_orders":false}`, 200, false},
		{"missing identity", `{"version":1,"idempotent_orders":true}`, 200, false},
		{"HTML", `<html>private diagnostic</html>`, 200, false},
		{"unsupported endpoint", `private diagnostic`, 404, false},
		{"auth expired", `private diagnostic`, 401, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/widget/freedompost_handoff" || r.Header.Get("X-Auth-Token") != "signed-widget-token" || r.URL.Query().Get("website_token") != "website-token" {
					t.Errorf("wrong authenticated capability request")
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer s.Close()
			c, err := New(s.URL, "website-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = s.Client().Transport
			capability, err := c.HandoffCapability(context.Background(), "signed-widget-token")
			if test.valid {
				if err != nil || capability.BindingID != handoffBinding {
					t.Fatalf("valid capability rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe capability accepted")
			}
			if err != nil && (strings.Contains(err.Error(), "private diagnostic") || strings.Contains(err.Error(), "signed-widget-token") || strings.Contains(err.Error(), "website-token")) {
				t.Fatal("sensitive diagnostic leaked")
			}
		})
	}
}

func TestHandoffSendsStableKeyAndValidatesReceipt(t *testing.T) {
	valid := fmt.Sprintf(`{"id":123,"content":"stored order","message_type":0,"private":false,"freedompost_delivery":{"version":1,"binding_id":%q,"conversation_id":37,"request_key":%q}}`, handoffBinding, handoffOrderID)
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", valid, true},
		{"legacy response", `{"id":123,"content":"stored order","message_type":0,"private":false}`, false},
		{"wrong binding", strings.Replace(valid, handoffBinding, "another-visitor", 1), false},
		{"wrong key", strings.Replace(valid, handoffOrderID, "5ac80dfe-eb09-40d4-9c4f-cb4a343ac03f", 1), false},
		{"wrong conversation", strings.Replace(valid, `"conversation_id":37`, `"conversation_id":0`, 1), false},
		{"wrong content", strings.Replace(valid, "stored order", "altered order", 1), false},
		{"private", strings.Replace(valid, `"private":false`, `"private":true`, 1), false},
		{"outgoing", strings.Replace(valid, `"message_type":0`, `"message_type":1`, 1), false},
		{"no message id", strings.Replace(valid, `"id":123`, `"id":0`, 1), false},
		{"trailing JSON", valid + `{}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/widget/freedompost_handoff/messages" || r.Header.Get("X-Auth-Token") != "signed-widget-token" {
					t.Errorf("wrong authenticated message request")
				}
				var input struct {
					Message map[string]any `json:"message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if input.Message["freedompost_delivery_key"] != handoffOrderID || input.Message["content"] != "stored order" {
					t.Error("stable key or stored content absent")
				}
				for _, forbidden := range []string{"conversation_id", "contact_id", "message_type", "private"} {
					if _, ok := input.Message[forbidden]; ok {
						t.Errorf("unauthorized routing field %s", forbidden)
					}
				}
				fmt.Fprint(w, test.body)
			}))
			defer s.Close()
			c, err := New(s.URL, "website-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = s.Client().Transport
			receipt, err := c.SendOrderHandoff(context.Background(), "signed-widget-token", handoffOrderID, handoffBinding, "stored order", "https://shop.example/market/")
			if test.valid {
				if err != nil || receipt.MessageID != 123 || receipt.ConversationID != 37 {
					t.Fatalf("valid receipt failed: %v", err)
				}
			} else {
				var failure *HandoffError
				if !errors.As(err, &failure) || !failure.UnknownOutcome || !failure.Retryable {
					t.Fatalf("ambiguous receipt must reconcile with same key: %v", err)
				}
			}
		})
	}
}

func TestHandoffUnknownResponseAndStatusClassification(t *testing.T) {
	for _, status := range []int{401, 404, 405, 409, 429, 500, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private signed-widget-token website-token")
			}))
			defer s.Close()
			c, _ := New(s.URL, "website-token", time.Second)
			c.http.Transport = s.Client().Transport
			_, err := c.SendOrderHandoff(context.Background(), "signed-widget-token", handoffOrderID, handoffBinding, "stored order", "")
			var failure *HandoffError
			if !errors.As(err, &failure) || failure.UnknownOutcome != (status >= 500) || failure.Retryable != (status == 429 || status >= 500) {
				t.Fatalf("wrong status classification: %v", err)
			}
			if strings.Contains(err.Error(), "token") {
				t.Fatal("leaked secret in failure")
			}
		})
	}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker := w.(http.Hijacker)
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer s.Close()
	c, _ := New(s.URL, "website-token", time.Second)
	c.http.Transport = s.Client().Transport
	_, err := c.SendOrderHandoff(context.Background(), "signed-widget-token", handoffOrderID, handoffBinding, "stored order", "")
	var failure *HandoffError
	if !errors.As(err, &failure) || !failure.UnknownOutcome || !failure.Retryable {
		t.Fatalf("lost response must not be treated as not sent: %v", err)
	}
}

func TestHandoffDowngradeAfterCapabilityCannotWriteThroughLegacyRoute(t *testing.T) {
	var upgraded atomic.Bool
	var legacyWrites, durableWrites, dedicatedPosts atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/widget/freedompost_handoff":
			fmt.Fprintf(w, `{"version":1,"binding_id":%q,"idempotent_orders":true}`, handoffBinding)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/widget/messages":
			// A legacy receiver ignores the unknown delivery key and writes a
			// normal message. The order sender must never reach this fallback.
			legacyWrites.Add(1)
			fmt.Fprint(w, `{"id":123,"content":"stored order","message_type":0,"private":false}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/widget/freedompost_handoff/messages":
			dedicatedPosts.Add(1)
			if !upgraded.Load() {
				http.NotFound(w, r)
				return
			}
			var input struct {
				Message struct {
					Key string `json:"freedompost_delivery_key"`
				} `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Message.Key != handoffOrderID {
				t.Error("recovery changed the original delivery key")
				http.Error(w, "invalid test input", http.StatusUnprocessableEntity)
				return
			}
			durableWrites.CompareAndSwap(0, 1)
			fmt.Fprintf(w, `{"id":123,"content":"stored order","message_type":0,"private":false,"freedompost_delivery":{"version":1,"binding_id":%q,"conversation_id":37,"request_key":%q}}`, handoffBinding, handoffOrderID)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, "website-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = s.Client().Transport
	if _, err := c.HandoffCapability(context.Background(), "signed-widget-token"); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.SendOrderHandoff(context.Background(), "signed-widget-token", handoffOrderID, handoffBinding, "stored order", "")
	if legacyWrites.Load() != 0 {
		t.Fatal("downgrade wrote an untracked order through the legacy message route")
	}
	var failure *HandoffError
	if receipt != nil || !errors.As(err, &failure) || failure.Code != "SUPPORT_HTTP_404" || failure.UnknownOutcome || failure.Retryable || durableWrites.Load() != 0 {
		t.Fatalf("legacy receiver must fail without a message or fallback: receipt=%v error=%v", receipt, err)
	}
	upgraded.Store(true)
	for range 2 {
		receipt, err = c.SendOrderHandoff(context.Background(), "signed-widget-token", handoffOrderID, handoffBinding, "stored order", "")
		if err != nil || receipt.MessageID != 123 {
			t.Fatalf("original order recovery failed: receipt=%v error=%v", receipt, err)
		}
	}
	if legacyWrites.Load() != 0 || durableWrites.Load() != 1 || dedicatedPosts.Load() != 3 {
		t.Fatalf("unsafe delivery routing: legacy=%d durable=%d dedicated=%d", legacyWrites.Load(), durableWrites.Load(), dedicatedPosts.Load())
	}
}

func TestHandoffDoesNotForwardSignedTokenAcrossRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	c, _ := New(s.URL, "website-token", time.Second)
	c.http.Transport = s.Client().Transport
	_, err := c.HandoffCapability(context.Background(), "signed-widget-token")
	if err == nil || redirected {
		t.Fatal("authenticated request followed redirect")
	}
}
