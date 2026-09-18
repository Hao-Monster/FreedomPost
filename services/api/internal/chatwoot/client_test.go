package chatwoot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendOrderUsesWebWidgetMessageAPI(t *testing.T) {
	const (
		websiteToken      = "website-token"
		conversationToken = "signed-conversation-token"
		content           = "你好，订单 FP123，请协助处理。"
		referer           = "https://freedompost.example/market/"
	)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/widget/messages" {
			t.Errorf("request = %s %s, want POST /api/v1/widget/messages", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("website_token"); got != websiteToken {
			t.Errorf("website_token = %q, want %q", got, websiteToken)
		}
		if got := r.URL.Query().Get("locale"); got != "zh_CN" {
			t.Errorf("locale = %q, want zh_CN", got)
		}
		if got := r.URL.Query().Get("cw_conversation"); got != "" {
			t.Errorf("conversation token leaked into query: %q", got)
		}
		if got := r.Header.Get("X-Auth-Token"); got != conversationToken {
			t.Errorf("X-Auth-Token = %q, want conversation token", got)
		}
		if got := r.Header.Get("api_access_token"); got != "" {
			t.Errorf("unexpected legacy api_access_token header %q", got)
		}
		var body struct {
			Message map[string]any `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := body.Message["content"]; got != content {
			t.Errorf("content = %v, want %q", got, content)
		}
		if got := body.Message["referer_url"]; got != referer {
			t.Errorf("referer_url = %v, want %q", got, referer)
		}
		if timestamp, ok := body.Message["timestamp"].(string); !ok || timestamp == "" {
			t.Errorf("timestamp = %v, want non-empty string", body.Message["timestamp"])
		}
		if _, ok := body.Message["message_type"]; ok {
			t.Error("message_type must be assigned by the WebWidget API")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := New(server.URL, websiteToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	if err := client.SendOrder(context.Background(), conversationToken, content, referer); err != nil {
		t.Fatal(err)
	}
}

func TestSendOrderReportsChatwootFailureWithoutResponseBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"private diagnostic"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := New(server.URL, "website-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	err = client.SendOrder(context.Background(), "signed-token", "order", "")
	if err == nil || err.Error() != "chatwoot status 401" {
		t.Fatalf("error = %v, want status-only error", err)
	}
}

func TestNewRejectsIncompleteOrUnsafeConfiguration(t *testing.T) {
	for name, pair := range map[string][2]string{
		"empty":            {"", ""},
		"missing base":     {"", "website-token"},
		"missing token":    {"https://support.example", ""},
		"insecure base":    {"http://support.example", "website-token"},
		"base credentials": {"https://user:pass@support.example", "website-token"},
		"token newline":    {"https://support.example", "website\ntoken"},
	} {
		t.Run(name, func(t *testing.T) {
			base, websiteToken := pair[0], pair[1]
			client, err := New(base, websiteToken, time.Second)
			if name == "empty" {
				if err != nil || client != nil {
					t.Fatalf("New() = (%v, %v), want (nil, nil)", client, err)
				}
				return
			}
			if err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want configuration error", client, err)
			}
		})
	}
}

func TestSendOrderRejectsInvalidConversationToken(t *testing.T) {
	client, err := New("https://support.example", "website-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "bad\nvalue"} {
		if err := client.SendOrder(context.Background(), token, "order", ""); err == nil {
			t.Errorf("SendOrder(%q) succeeded, want token validation error", token)
		}
	}
}
