package httpapi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/channelbridge"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/wecom"
)

const httpBridgeAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
const httpBridgePKCS7BlockSize = 32

func TestChatwootBridgeWebhookHandlerVerifiesAndForwards(t *testing.T) {
	var received string
	wecomServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_, _ = io.WriteString(w, `{"errcode":0,"access_token":"token","expires_in":7200}`)
		case "/cgi-bin/message/send":
			var payload struct {
				Text struct {
					Content string `json:"content"`
				} `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			received = payload.Text.Content
			_, _ = io.WriteString(w, `{"errcode":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer wecomServer.Close()

	client, err := wecom.New(wecom.Config{
		BaseURL:        wecomServer.URL,
		CorpID:         "corp",
		CorpSecret:     "secret",
		AgentID:        1,
		CallbackToken:  "callback",
		EncodingAESKey: httpBridgeAESKey,
		HTTPClient:     wecomServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	chatwootServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer chatwootServer.Close()
	chatwootClient, err := channelbridge.NewChatwootClient(channelbridge.ChatwootConfig{
		BaseURL:   chatwootServer.URL,
		APIToken:  "api-token",
		AccountID: 5,
		HTTP:      chatwootServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := channelbridge.NewService(channelbridge.ServiceConfig{
		Chatwoot:       chatwootClient,
		WeCom:          client,
		OperatorUserID: "operator",
		WebhookSecret:  "webhook-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{channelBridge: service}

	body := `{"event":"message_created","id":7,"message_type":"incoming","content":"订单 FP42","conversation":{"id":42},"account":{"id":5}}`
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := chatwootSignature("webhook-secret", timestamp, body)
	request := httptest.NewRequest(http.MethodPost, "/api/integrations/chatwoot/webhook", strings.NewReader(body))
	request.Header.Set("X-Chatwoot-Timestamp", timestamp)
	request.Header.Set("X-Chatwoot-Signature", signature)
	recorder := httptest.NewRecorder()
	server.chatwootBridgeWebhook(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if received != "FP-CW:42\n订单 FP42" {
		t.Fatalf("received = %q", received)
	}

	badRequest := httptest.NewRequest(http.MethodPost, "/api/integrations/chatwoot/webhook", strings.NewReader(body))
	badRequest.Header.Set("X-Chatwoot-Timestamp", timestamp)
	badRequest.Header.Set("X-Chatwoot-Signature", "sha256="+strings.Repeat("0", 64))
	badRecorder := httptest.NewRecorder()
	server.chatwootBridgeWebhook(badRecorder, badRequest)
	if badRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d", badRecorder.Code)
	}
}

func TestWeComCallbackHandlerDecryptsAndRoutesReply(t *testing.T) {
	var received string
	chatwootServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		received = payload.Content
		w.WriteHeader(http.StatusCreated)
	}))
	defer chatwootServer.Close()
	chatwootClient, err := channelbridge.NewChatwootClient(channelbridge.ChatwootConfig{
		BaseURL:   chatwootServer.URL,
		APIToken:  "api-token",
		AccountID: 5,
		HTTP:      chatwootServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wecomServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer wecomServer.Close()
	wecomClient, err := wecom.New(wecom.Config{
		BaseURL:        wecomServer.URL,
		CorpID:         "corp",
		CorpSecret:     "secret",
		AgentID:        1,
		CallbackToken:  "callback",
		EncodingAESKey: httpBridgeAESKey,
		HTTPClient:     wecomServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := channelbridge.NewService(channelbridge.ServiceConfig{
		Chatwoot:       chatwootClient,
		WeCom:          wecomClient,
		OperatorUserID: "operator",
		WebhookSecret:  "webhook-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{channelBridge: service}

	plain := []byte(`<xml><ToUserName><![CDATA[corp]]></ToUserName><FromUserName><![CDATA[operator]]></FromUserName><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[FP-CW:42 已收到]]></Content><MsgId>m1</MsgId></xml>`)
	encrypted := encryptHTTPBridgePayload(t, httpBridgeAESKey, plain, "corp")
	timestamp, nonce := fmt.Sprintf("%d", time.Now().Unix()), "nonce"
	query := url.Values{}
	query.Set("msg_signature", wecomSignature("callback", timestamp, nonce, encrypted))
	query.Set("timestamp", timestamp)
	query.Set("nonce", nonce)
	request := httptest.NewRequest(http.MethodPost, "/api/integrations/wecom/callback?"+query.Encode(), strings.NewReader(`<xml><Encrypt><![CDATA[`+encrypted+`]]></Encrypt></xml>`))
	recorder := httptest.NewRecorder()
	server.wecomCallback(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "success" {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if received != "已收到" {
		t.Fatalf("received = %q", received)
	}
}

func chatwootSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, timestamp+"."+body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func wecomSignature(token, timestamp, nonce, encrypted string) string {
	values := []string{token, timestamp, nonce, encrypted}
	sort.Strings(values)
	digest := sha1.Sum([]byte(strings.Join(values, "")))
	return fmt.Sprintf("%x", digest[:])
}

func encryptHTTPBridgePayload(t *testing.T, keyText string, message []byte, receiverID string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(keyText + "=")
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 16+4+len(message)+len(receiverID))
	for i := range plain[:16] {
		plain[i] = byte(i + 1)
	}
	binary.BigEndian.PutUint32(plain[16:20], uint32(len(message)))
	copy(plain[20:], message)
	copy(plain[20+len(message):], receiverID)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	// WeCom's protocol pads to 32 bytes; AES-CBC still encrypts 16-byte blocks.
	padding := httpBridgePKCS7BlockSize - len(plain)%httpBridgePKCS7BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext)
}
