// Package channelbridge connects the existing Chatwoot conversations to an
// external customer-service channel. The bridge is intentionally independent
// from order creation and is enabled only after its credentials are validated.
package channelbridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxWebhookBodyBytes = 1 << 20

// ChatwootConfig configures the server-side Chatwoot API client.
type ChatwootConfig struct {
	BaseURL   string
	APIToken  string
	AccountID int64
	Timeout   time.Duration
	HTTP      *http.Client
}

type ChatwootClient struct {
	baseURL   *url.URL
	apiToken  string
	accountID int64
	http      *http.Client
}

// NewChatwootClient validates an API origin and creates a client. The API
// token is never included in returned errors.
func NewChatwootClient(cfg ChatwootConfig) (*ChatwootClient, error) {
	base := cfg.BaseURL
	if base == "" {
		return nil, errors.New("chatwoot base URL is required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("chatwoot base URL must be an HTTPS origin")
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, errors.New("chatwoot API token is required")
	}
	if cfg.AccountID <= 0 {
		return nil, errors.New("chatwoot account ID must be positive")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if timeout < 100*time.Millisecond || timeout > 2*time.Minute {
		return nil, errors.New("chatwoot timeout must be between 100ms and 2m")
	}
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are disabled for chatwoot requests")
		}}
	}
	u.Path = "/"
	return &ChatwootClient{baseURL: u, apiToken: strings.TrimSpace(cfg.APIToken), accountID: cfg.AccountID, http: httpClient}, nil
}

// SendOutgoingMessage adds an operator reply to an existing conversation.
func (c *ChatwootClient) SendOutgoingMessage(ctx context.Context, conversationID int64, content string) error {
	if conversationID <= 0 {
		return errors.New("chatwoot conversation ID must be positive")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("chatwoot message content is required")
	}
	body, err := json.Marshal(map[string]any{
		"content":      content,
		"message_type": "outgoing",
		"private":      false,
	})
	if err != nil {
		return errors.New("chatwoot message could not be encoded")
	}
	path := fmt.Sprintf("api/v1/accounts/%d/conversations/%d/messages", c.accountID, conversationID)
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(string(body)))
	if err != nil {
		return errors.New("chatwoot request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")
	// Rack normalizes hyphens to underscores; proxies may drop underscore headers.
	request.Header.Set("api-access-token", c.apiToken)
	started := time.Now()
	logger := slog.Default().With("target_host", c.baseURL.Host, "account_id", c.accountID, "conversation_id", conversationID, "stage", "create_message")
	logger.Info("chatwoot outgoing request", "auth_header_present", request.Header.Get("api-access-token") != "")
	response, err := c.http.Do(request)
	if err != nil {
		kind := "transport_error"
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		var ue *url.Error
		if errors.As(err, &ue) && strings.Contains(ue.Err.Error(), "redirects are disabled") {
			kind = "redirect_blocked"
		}
		logger.Error("chatwoot outgoing transport failed", "reason", kind, "duration_ms", time.Since(started).Milliseconds())
		return errors.New("chatwoot request failed")
	}
	defer response.Body.Close()
	responseBody, err := readBounded(response.Body, maxWebhookBodyBytes)
	if err != nil {
		logger.Error("chatwoot outgoing response unreadable", "status", response.StatusCode)
		return errors.New("chatwoot response exceeded size limit")
	}
	// Log only fixed classifications: upstream bodies and headers may contain secrets.
	logger.Info("chatwoot outgoing response", "status", response.StatusCode, "duration_ms", time.Since(started).Milliseconds(), "response_kind", responseKind(responseBody), "edge_present", response.Header.Get("CF-Ray") != "", "request_id_present", response.Header.Get("X-Request-Id") != "")
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("chatwoot request failed (status %d)", response.StatusCode)
	}
	return nil
}

func (c *ChatwootClient) SendOutgoingImage(ctx context.Context, conversationID int64, media []byte, filename, contentType string) error {
	if conversationID <= 0 || len(media) == 0 {
		return errors.New("chatwoot image is invalid")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("message_type", "outgoing")
	_ = writer.WriteField("private", "false")
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[]"; filename="%s"`, strings.ReplaceAll(filename, `"`, "")))
	headers.Set("Content-Type", contentType)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return errors.New("chatwoot image could not be encoded")
	}
	if _, err := part.Write(media); err != nil {
		return errors.New("chatwoot image could not be encoded")
	}
	if err := writer.Close(); err != nil {
		return errors.New("chatwoot image could not be encoded")
	}
	path := fmt.Sprintf("api/v1/accounts/%d/conversations/%d/messages", c.accountID, conversationID)
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), &body)
	if err != nil {
		return errors.New("chatwoot image request could not be created")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("api-access-token", c.apiToken)
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("chatwoot image request failed")
	}
	defer response.Body.Close()
	if _, err := readBounded(response.Body, maxWebhookBodyBytes); err != nil {
		return errors.New("chatwoot image response exceeded size limit")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("chatwoot image request failed (status %d)", response.StatusCode)
	}
	return nil
}

func responseKind(body []byte) string {
	if json.Valid(body) {
		return "json"
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(body))), "<!doctype html") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(body))), "<html") {
		return "html"
	}
	if len(body) == 0 {
		return "empty"
	}
	return "other"
}

// VerifyWebhookSignature validates Chatwoot's timestamped HMAC signature.
func VerifyWebhookSignature(rawBody, timestamp, signature, secret string, now time.Time, tolerance time.Duration) bool {
	secret = strings.TrimSpace(secret)
	if secret == "" || rawBody == "" || timestamp == "" || signature == "" {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || tolerance < 0 {
		return false
	}
	if tolerance == 0 {
		tolerance = 5 * time.Minute
	}
	if delta := now.Unix() - seconds; delta > int64(tolerance/time.Second) || delta < -int64(tolerance/time.Second) {
		return false
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided := strings.TrimPrefix(signature, "sha256=")
	if len(provided) != sha256.Size*2 {
		return false
	}
	providedBytes, err := hex.DecodeString(provided)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, timestamp)
	_, _ = io.WriteString(mac, ".")
	_, _ = io.WriteString(mac, rawBody)
	return hmac.Equal(providedBytes, mac.Sum(nil))
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("body exceeds size limit")
	}
	return body, nil
}
