package channelbridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/wecom"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := `{"event":"message_created"}`
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(timestamp + "." + body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifyWebhookSignature(body, timestamp, signature, "secret", time.Now(), 5*time.Minute) {
		t.Fatal("expected valid signature")
	}
	if VerifyWebhookSignature(body, timestamp, strings.TrimPrefix(signature, "sha256="), "secret", time.Now(), 5*time.Minute) {
		t.Fatal("signature without sha256 prefix accepted")
	}
	if VerifyWebhookSignature(body+" ", timestamp, signature, "secret", time.Now(), 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
}

func TestBridgeRoutesBothDirectionsWithConversationReference(t *testing.T) {
	var sentToWecom string
	var sentToChatwoot string
	wecomServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"token","expires_in":7200}`))
		case "/cgi-bin/message/send":
			var payload struct {
				Touser string `json:"touser"`
				Text   struct {
					Content string `json:"content"`
				} `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sentToWecom = payload.Touser + ":" + payload.Text.Content
			_, _ = w.Write([]byte(`{"errcode":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer wecomServer.Close()
	chatwootServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		sentToChatwoot, _ = payload["content"].(string)
		w.WriteHeader(http.StatusCreated)
	}))
	defer chatwootServer.Close()

	wc, err := wecom.New(wecom.Config{BaseURL: wecomServer.URL, CorpID: "corp", CorpSecret: "secret", AgentID: 1, CallbackToken: "callback", EncodingAESKey: testAESKey, HTTPClient: wecomServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := NewChatwootClient(ChatwootConfig{BaseURL: chatwootServer.URL, APIToken: "api-token", AccountID: 5, HTTP: chatwootServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Chatwoot: cc, WeCom: wc, OperatorUserID: "operator", WebhookSecret: "secret", Mappings: fixedConversationStore{id: 42}, AllowUnprefixedReplies: true})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"event":"message_created","id":7,"message_type":"incoming","content":"订单 FP42","conversation":{"id":42},"account":{"id":5}}`
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(timestamp + "." + body))
	if err := service.HandleChatwootWebhook(context.Background(), body, timestamp, "sha256="+hex.EncodeToString(mac.Sum(nil))); err != nil {
		t.Fatal(err)
	}
	if sentToWecom != "operator:FP-CW:42\n订单 FP42" {
		t.Fatalf("sentToWecom = %q", sentToWecom)
	}
	if err := service.HandleWeComMessage(context.Background(), wecom.Message{FromUserName: "operator", MsgType: "text", MsgID: "m1", Content: "FP-CW:42 已收到，请稍候"}); err != nil {
		t.Fatal(err)
	}
	if sentToChatwoot != "已收到，请稍候" {
		t.Fatalf("sentToChatwoot = %q", sentToChatwoot)
	}
	if err := service.HandleWeComMessage(context.Background(), wecom.Message{FromUserName: "operator", MsgType: "text", MsgID: "m2", Content: "不带标识的回复"}); err != nil {
		t.Fatal(err)
	}
	if sentToChatwoot != "不带标识的回复" {
		t.Fatalf("unprefixed sentToChatwoot = %q", sentToChatwoot)
	}
}

type fixedConversationStore struct{ id int64 }

func (s fixedConversationStore) Put(context.Context, string, int64, time.Duration) error { return nil }
func (s fixedConversationStore) Latest(context.Context, string) (int64, error)           { return s.id, nil }

const testAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
