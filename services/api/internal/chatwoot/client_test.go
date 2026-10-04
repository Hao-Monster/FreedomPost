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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 123, "content": content, "message_type": 0, "private": false,
		})
	}))
	defer server.Close()

	client, err := New(server.URL, websiteToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	if err := client.SendOrder(context.Background(), conversationToken, "\n"+content+" \t", referer); err != nil {
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
	client.http.Transport = server.Client().Transport
	err = client.SendOrder(context.Background(), "signed-token", "order", "")
	if err == nil || err.Error() != "chatwoot status 401" {
		t.Fatalf("error = %v, want status-only error", err)
	}
}

func TestSendOrderValidatesCreatedMessage(t *testing.T) {
	const validResponse = `{"id":123,"content":"order","message_type":0,"private":false}`
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"valid response", http.StatusOK, validResponse, ""},
		{"HTML success", http.StatusOK, "<html>private diagnostic</html>", "invalid chatwoot message response"},
		{"empty created", http.StatusCreated, "", "invalid chatwoot message response"},
		{"no content", http.StatusNoContent, "", "invalid chatwoot message response"},
		{"null response", http.StatusOK, "null", "invalid chatwoot message response"},
		{"missing id", http.StatusOK, `{"content":"order","message_type":0,"private":false}`, "invalid chatwoot message response"},
		{"zero id", http.StatusOK, strings.Replace(validResponse, "123", "0", 1), "invalid chatwoot message response"},
		{"negative id", http.StatusOK, strings.Replace(validResponse, "123", "-1", 1), "invalid chatwoot message response"},
		{"fractional id", http.StatusOK, strings.Replace(validResponse, "123", "1.5", 1), "invalid chatwoot message response"},
		{"different content", http.StatusOK, strings.Replace(validResponse, "order", "another order", 1), "invalid chatwoot message response"},
		{"missing message type", http.StatusOK, `{"id":123,"content":"order","private":false}`, "invalid chatwoot message response"},
		{"null message type", http.StatusOK, strings.Replace(validResponse, `"message_type":0`, `"message_type":null`, 1), "invalid chatwoot message response"},
		{"outgoing message", http.StatusOK, strings.Replace(validResponse, `"message_type":0`, `"message_type":1`, 1), "invalid chatwoot message response"},
		{"private message", http.StatusOK, strings.Replace(validResponse, "false", "true", 1), "invalid chatwoot message response"},
		{"missing privacy", http.StatusOK, `{"id":123,"content":"order","message_type":0}`, "invalid chatwoot message response"},
		{"trailing JSON", http.StatusOK, validResponse + `{}`, "invalid chatwoot message response"},
		{"oversized response", http.StatusOK, validResponse + strings.Repeat(" ", 1<<20), "chatwoot message response is too large"},
		{"rejected response", http.StatusNotFound, `{"error":"private diagnostic website-token signed-token"}`, "chatwoot status 404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "website-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport = server.Client().Transport
			err = client.SendOrder(context.Background(), "signed-token", "order", "")
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSendOrderDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirect-target" {
					redirected.Add(1)
					_, _ = w.Write([]byte(`{"id":123,"content":"order","message_type":0,"private":false}`))
					return
				}
				http.Redirect(w, r, "/redirect-target", status)
			}))
			defer server.Close()
			client, err := New(server.URL, "website-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport = server.Client().Transport
			err = client.SendOrder(context.Background(), "signed-token", "order", "")
			if err == nil || err.Error() != fmt.Sprintf("chatwoot status %d", status) {
				t.Errorf("error = %v, want redirect status %d", err, status)
			}
			if got := redirected.Load(); got != 0 {
				t.Errorf("redirect target received %d requests, want none", got)
			}
		})
	}
}

func TestSendOrderRedactsTransportError(t *testing.T) {
	client, err := New("https://support.example", "website-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("private diagnostic " + r.URL.String() + " " + r.Header.Get("X-Auth-Token"))
	})
	err = client.SendOrder(context.Background(), "signed-token", "order", "")
	if err == nil || err.Error() != "send chatwoot message failed" {
		t.Fatalf("error = %v, want redacted transport failure", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
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
