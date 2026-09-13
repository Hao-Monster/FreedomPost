package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

const testAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func TestSendTextGetsTokenAndUsesAgentAPI(t *testing.T) {
	var gotMessage map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			if r.URL.Query().Get("corpid") != "corp" || r.URL.Query().Get("corpsecret") != "corp-secret" {
				t.Fatalf("unexpected token query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"access-secret","expires_in":7200}`))
		case "/cgi-bin/message/send":
			if r.URL.Query().Get("access_token") != "access-secret" {
				t.Fatalf("unexpected access token")
			}
			if err := json.NewDecoder(r.Body).Decode(&gotMessage); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:        server.URL,
		CorpID:         "corp",
		CorpSecret:     "corp-secret",
		AgentID:        1001,
		CallbackToken:  "callback-token",
		EncodingAESKey: testAESKey,
		HTTPClient:     server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendText(context.Background(), "operator", "FP-CW:42\n订单 FP42"); err != nil {
		t.Fatal(err)
	}
	if gotMessage["touser"] != "operator" || gotMessage["agentid"] != float64(1001) {
		t.Fatalf("unexpected message: %#v", gotMessage)
	}
	text, ok := gotMessage["text"].(map[string]any)
	if !ok || text["content"] != "FP-CW:42\n订单 FP42" {
		t.Fatalf("unexpected text: %#v", gotMessage["text"])
	}
}

func TestDecryptCallbackAndVerifyEcho(t *testing.T) {
	client, err := New(Config{
		CorpID:         "corp",
		CorpSecret:     "corp-secret",
		AgentID:        1001,
		CallbackToken:  "callback-token",
		EncodingAESKey: testAESKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`<xml><ToUserName><![CDATA[corp]]></ToUserName><FromUserName><![CDATA[operator]]></FromUserName><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[FP-CW:42 请处理]]></Content><AgentID>1001</AgentID></xml>`)
	encrypted := encryptTest(t, client.aesKey, plain, "corp")
	timestamp, nonce := "1789296000", "nonce"
	signature := signatureFor(client.callbackToken, timestamp, nonce, encrypted)
	message, err := client.DecryptCallback(signature, timestamp, nonce, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if message.FromUserName != "operator" || message.Content != "FP-CW:42 请处理" {
		t.Fatalf("unexpected callback: %#v", message)
	}
	echo, err := client.VerifyEcho(signatureFor(client.callbackToken, timestamp, nonce, encrypted), timestamp, nonce, encrypted)
	if err != nil || echo != string(plain) {
		t.Fatalf("VerifyEcho = %q, %v", echo, err)
	}
}

func TestRejectsUnsafeConfigurationAndBadSignature(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://example.test", CorpID: "corp", CorpSecret: "secret", AgentID: 1, CallbackToken: "token", EncodingAESKey: testAESKey}); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
	client, err := New(Config{CorpID: "corp", CorpSecret: "secret", AgentID: 1, CallbackToken: "token", EncodingAESKey: testAESKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DecryptCallback("bad", "1", "nonce", "ciphertext"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected signature error: %v", err)
	}
}

func encryptTest(t *testing.T, key, message []byte, receiverID string) string {
	t.Helper()
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
	padded := pkcs7Pad(plain, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(key)[:aes.BlockSize]).CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func pkcs7Pad(value []byte, blockSize int) []byte {
	padding := blockSize - len(value)%blockSize
	return append(append([]byte(nil), value...), bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func signatureFor(token, timestamp, nonce, encrypted string) string {
	values := []string{token, timestamp, nonce, encrypted}
	sort.Strings(values)
	digest := sha1.Sum([]byte(strings.Join(values, "")))
	return fmt.Sprintf("%x", digest[:])
}
