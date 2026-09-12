package chatwoot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendOrderCreatesContactFromNestedChatwootPayload(t *testing.T) {
	const (
		accountID int64 = 42
		inboxID   int64 = 7
		visitor         = "visitor-create@example.com"
		content         = "请联系我"
	)

	var contactPosts, conversationPosts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api_access_token") != "token" {
			t.Errorf("api_access_token = %q, want token", r.Header.Get("api_access_token"))
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/accounts/42/contacts/search":
			if got := r.URL.Query().Get("q"); got != visitor {
				t.Errorf("search q = %q, want %q", got, visitor)
			}
			writeJSON(t, w, map[string]any{"payload": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/accounts/42/contacts":
			contactPosts++
			var body struct {
				InboxID    int64  `json:"inbox_id"`
				Name       string `json:"name"`
				Identifier string `json:"identifier"`
			}
			decodeJSON(t, r, &body)
			if body.InboxID != inboxID || body.Name != "FreedomPost访客" || body.Identifier != visitor {
				t.Errorf("contact body = %+v", body)
			}
			writeJSON(t, w, map[string]any{"payload": map[string]any{
				"contact": map[string]any{"id": 123, "identifier": visitor},
				"contact_inbox": map[string]any{
					"source_id": "source-created",
					"inbox":     map[string]any{"id": inboxID},
				},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/accounts/42/conversations":
			conversationPosts++
			assertConversation(t, r, "source-created", 123, inboxID, content)
			writeJSON(t, w, map[string]any{"id": 9001})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "token", accountID, inboxID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()

	if err := client.SendOrder(context.Background(), visitor, content); err != nil {
		t.Fatal(err)
	}
	if contactPosts != 1 || conversationPosts != 1 {
		t.Fatalf("contact posts = %d, conversation posts = %d; want 1 and 1", contactPosts, conversationPosts)
	}
}

func TestSendOrderReusesExistingContactFromSearch(t *testing.T) {
	const (
		accountID int64 = 42
		inboxID   int64 = 7
		visitor         = "visitor-existing@example.com"
		content         = "已有联系人消息"
	)

	var contactPosts, conversationPosts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/accounts/42/contacts/search":
			if got := r.URL.Query().Get("q"); got != visitor {
				t.Errorf("search q = %q, want %q", got, visitor)
			}
			writeJSON(t, w, map[string]any{"payload": []any{map[string]any{
				"id":         456,
				"identifier": visitor,
				"contact_inboxes": []any{map[string]any{
					"source_id": "source-existing",
					"inbox":     map[string]any{"id": inboxID},
				}},
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/accounts/42/contacts":
			contactPosts++
			http.Error(w, "contact creation should not be called", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/accounts/42/conversations":
			conversationPosts++
			assertConversation(t, r, "source-existing", 456, inboxID, content)
			writeJSON(t, w, map[string]any{"id": 9002})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "token", accountID, inboxID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()

	if err := client.SendOrder(context.Background(), visitor, content); err != nil {
		t.Fatal(err)
	}
	if contactPosts != 0 || conversationPosts != 1 {
		t.Fatalf("contact posts = %d, conversation posts = %d; want 0 and 1", contactPosts, conversationPosts)
	}
}

func assertConversation(t *testing.T, r *http.Request, sourceID string, contactID, inboxID int64, content string) {
	t.Helper()
	var body struct {
		SourceID  string `json:"source_id"`
		ContactID int64  `json:"contact_id"`
		InboxID   int64  `json:"inbox_id"`
		Status    string `json:"status"`
		Message   struct {
			Content     string `json:"content"`
			MessageType string `json:"message_type"`
			ContentType string `json:"content_type"`
			Private     bool   `json:"private"`
		} `json:"message"`
	}
	decodeJSON(t, r, &body)
	if body.SourceID != sourceID || body.ContactID != contactID || body.InboxID != inboxID || body.Status != "open" {
		t.Errorf("conversation body = %+v", body)
	}
	if body.Message.Content != content || body.Message.MessageType != "incoming" || body.Message.ContentType != "text" || body.Message.Private {
		t.Errorf("conversation message = %+v", body.Message)
	}
}

func decodeJSON(t *testing.T, r *http.Request, value any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		t.Fatalf("decode request JSON: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response JSON: %v", err)
	}
}
