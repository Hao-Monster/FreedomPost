// Package wecom contains the small, server-side subset of the official
// WeCom Agent API needed by the Chatwoot bridge. It deliberately does not
// depend on OpenClaw or on a desktop WeCom client.
package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL        = "https://qyapi.weixin.qq.com"
	defaultTimeout        = 15 * time.Second
	maxResponseBytes      = 1 << 20
	maxTextBytes          = 2048
	accessTokenSafetyTime = 60 * time.Second
	// WeCom's WXBizMsgCrypt protocol uses a 32-byte PKCS#7 padding block.
	// This is distinct from AES's 16-byte cipher block size used by CBC.
	wecomPKCS7BlockSize = 32
)

// Config contains the credentials and callback settings for one WeCom Agent.
// All values are server-side secrets and must be supplied through the runtime
// environment, never through a browser request.
type Config struct {
	BaseURL        string
	CorpID         string
	CorpSecret     string
	AgentID        int64
	CallbackToken  string
	EncodingAESKey string
	ReceiveID      string
	Timeout        time.Duration
	HTTPClient     *http.Client
}

// Client is safe for concurrent use. Access tokens are cached and refreshed
// under a mutex to avoid a token refresh stampede when messages arrive in a
// burst.
type Client struct {
	baseURL        *url.URL
	corpID         string
	corpSecret     string
	agentID        int64
	callbackToken  string
	aesKey         []byte
	receiveID      string
	http           *http.Client
	tokenMu        sync.Mutex
	cachedToken    string
	tokenExpiresAt time.Time
}

// Message is the plaintext subset of an Agent callback used by the bridge.
// PicURL and MediaID are retained for the next media-forwarding phase.
type Message struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   string `xml:"CreateTime"`
	MsgID        string `xml:"MsgId"`
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	PicURL       string `xml:"PicUrl"`
	MediaID      string `xml:"MediaId"`
	AgentID      string `xml:"AgentID"`
}

type callbackEnvelope struct {
	Encrypt string `xml:"Encrypt"`
}

type callbackPayload struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   string `xml:"CreateTime"`
	MsgID        string `xml:"MsgId"`
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	PicURL       string `xml:"PicUrl"`
	MediaID      string `xml:"MediaId"`
	AgentID      string `xml:"AgentID"`
}

type tokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type sendMessageResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// New validates an Agent configuration. HTTP is accepted only for loopback
// test servers; production calls must use an HTTPS origin.
func New(cfg Config) (*Client, error) {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("wecom base URL must be an absolute URL")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("wecom base URL must use HTTPS (HTTP is allowed only for loopback tests)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("wecom base URL must contain only an origin")
	}
	u.Path = "/"

	corpID := strings.TrimSpace(cfg.CorpID)
	corpSecret := strings.TrimSpace(cfg.CorpSecret)
	callbackToken := strings.TrimSpace(cfg.CallbackToken)
	if corpID == "" || corpSecret == "" || callbackToken == "" {
		return nil, errors.New("wecom CorpID, CorpSecret, and callback token are required")
	}
	if cfg.AgentID <= 0 {
		return nil, errors.New("wecom AgentID must be positive")
	}
	key, err := decodeEncodingAESKey(strings.TrimSpace(cfg.EncodingAESKey))
	if err != nil {
		return nil, err
	}
	receiveID := strings.TrimSpace(cfg.ReceiveID)
	if receiveID == "" {
		receiveID = corpID
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 100*time.Millisecond || timeout > 2*time.Minute {
		return nil, errors.New("wecom timeout must be between 100ms and 2m")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are disabled for wecom requests")
		}}
	}

	return &Client{
		baseURL:       u,
		corpID:        corpID,
		corpSecret:    corpSecret,
		agentID:       cfg.AgentID,
		callbackToken: callbackToken,
		aesKey:        key,
		receiveID:     receiveID,
		http:          httpClient,
	}, nil
}

// SendText sends a message to one internal WeCom user. The target must be a
// server-configured user ID; callers do not accept it from an unauthenticated
// browser request.
func (c *Client) SendText(ctx context.Context, toUserID, content string) error {
	toUserID = strings.TrimSpace(toUserID)
	content = strings.TrimSpace(content)
	if toUserID == "" {
		return errors.New("wecom recipient is required")
	}
	if content == "" {
		return errors.New("wecom message content is required")
	}
	if len([]byte(content)) > maxTextBytes {
		return errors.New("wecom message content exceeds 2048 bytes")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"touser":  toUserID,
		"msgtype": "text",
		"agentid": c.agentID,
		"text":    map[string]string{"content": content},
		"safe":    0,
	}
	var result sendMessageResponse
	if err := c.postJSON(ctx, "/cgi-bin/message/send", token, payload, &result); err != nil {
		return err
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom send rejected (code %d)", result.ErrCode)
	}
	return nil
}

// VerifyEcho decrypts the echostr sent by WeCom while saving a callback URL.
func (c *Client) VerifyEcho(msgSignature, timestamp, nonce, echostr string) (string, error) {
	if !verifySignature(c.callbackToken, msgSignature, timestamp, nonce, echostr) {
		return "", errors.New("wecom callback signature mismatch")
	}
	plain, receiverID, err := c.decrypt(echostr)
	if err != nil {
		return "", err
	}
	if receiverID != c.receiveID {
		return "", errors.New("wecom callback receiver mismatch")
	}
	return string(plain), nil
}

// DecryptCallback verifies and decrypts an Agent callback body. Signature
// verification must happen before XML parsing of the untrusted message.
func (c *Client) DecryptCallback(msgSignature, timestamp, nonce, encrypted string) (Message, error) {
	if !verifySignature(c.callbackToken, msgSignature, timestamp, nonce, encrypted) {
		return Message{}, errors.New("wecom callback signature mismatch")
	}
	plain, receiverID, err := c.decrypt(encrypted)
	if err != nil {
		return Message{}, err
	}
	if receiverID != c.receiveID {
		return Message{}, errors.New("wecom callback receiver mismatch")
	}
	var payload callbackPayload
	if err := xml.Unmarshal(plain, &payload); err != nil {
		return Message{}, errors.New("wecom callback XML is invalid")
	}
	return Message{
		ToUserName:   payload.ToUserName,
		FromUserName: payload.FromUserName,
		CreateTime:   payload.CreateTime,
		MsgID:        payload.MsgID,
		MsgType:      payload.MsgType,
		Content:      payload.Content,
		PicURL:       payload.PicURL,
		MediaID:      payload.MediaID,
		AgentID:      payload.AgentID,
	}, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.cachedToken != "" && time.Until(c.tokenExpiresAt) > accessTokenSafetyTime {
		return c.cachedToken, nil
	}
	query := url.Values{}
	query.Set("corpid", c.corpID)
	query.Set("corpsecret", c.corpSecret)
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: "/cgi-bin/gettoken", RawQuery: query.Encode()})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return "", errors.New("wecom token request could not be created")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", errors.New("wecom token request failed")
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxResponseBytes)
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 {
		return "", fmt.Errorf("wecom token request failed (status %d)", response.StatusCode)
	}
	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", errors.New("wecom token response is invalid")
	}
	if result.ErrCode != 0 || result.AccessToken == "" || result.ExpiresIn <= 0 {
		return "", fmt.Errorf("wecom token request rejected (code %d)", result.ErrCode)
	}
	c.cachedToken = result.AccessToken
	c.tokenExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return c.cachedToken, nil
}

func (c *Client) postJSON(ctx context.Context, path, accessToken string, payload any, target any) error {
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	query := requestURL.Query()
	query.Set("access_token", accessToken)
	requestURL.RawQuery = query.Encode()
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("wecom request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(string(body)))
	if err != nil {
		return errors.New("wecom request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("wecom request failed")
	}
	defer response.Body.Close()
	responseBody, err := readBounded(response.Body, maxResponseBytes)
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("wecom request failed (status %d)", response.StatusCode)
	}
	if err := json.Unmarshal(responseBody, target); err != nil {
		return errors.New("wecom response is invalid")
	}
	return nil
}

func (c *Client) decrypt(encoded string) ([]byte, string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, "", errors.New("wecom encrypted payload is invalid")
	}
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return nil, "", errors.New("wecom AES key is invalid")
	}
	plain := make([]byte, len(ciphertext))
	iv := c.aesKey[:aes.BlockSize]
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	plain, err = pkcs7Unpad(plain, wecomPKCS7BlockSize)
	if err != nil || len(plain) < 20 {
		return nil, "", errors.New("wecom decrypted payload is invalid")
	}
	messageLength := binary.BigEndian.Uint32(plain[16:20])
	end := uint64(20) + uint64(messageLength)
	if end > uint64(len(plain)) {
		return nil, "", errors.New("wecom decrypted message length is invalid")
	}
	receiverStart := int(end)
	receiverEnd := len(plain)
	for receiverEnd > receiverStart && plain[receiverEnd-1] == 0 {
		receiverEnd--
	}
	receiverID := string(plain[receiverStart:receiverEnd])
	return plain[20:end], receiverID, nil
}

func decodeEncodingAESKey(value string) ([]byte, error) {
	if len(value) != 43 {
		return nil, errors.New("wecom EncodingAESKey must contain 43 characters")
	}
	decoded, err := base64.StdEncoding.DecodeString(value + "=")
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("wecom EncodingAESKey is invalid")
	}
	return decoded, nil
}

func verifySignature(token, signature, timestamp, nonce, encrypted string) bool {
	if token == "" || signature == "" || timestamp == "" || nonce == "" || encrypted == "" {
		return false
	}
	values := []string{token, timestamp, nonce, encrypted}
	sort.Strings(values)
	hash := sha1.Sum([]byte(strings.Join(values, "")))
	return strings.EqualFold(fmt.Sprintf("%x", hash[:]), signature)
}

func pkcs7Unpad(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 || len(value)%blockSize != 0 {
		return nil, errors.New("invalid padding length")
	}
	padding := int(value[len(value)-1])
	if padding < 1 || padding > blockSize || padding > len(value) {
		return nil, errors.New("invalid padding")
	}
	for _, b := range value[len(value)-padding:] {
		if int(b) != padding {
			return nil, errors.New("invalid padding")
		}
	}
	return value[:len(value)-padding], nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}
