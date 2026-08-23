package benefit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// opus8.go — Opus8 integration client.
// Ported from TS opus8-client.ts with identical HMAC-SHA256 signing scheme.

const (
	opus8WebmasterBenefitPath     = "/api/integrations/freedompost/benefits/webmaster/claim"
	opus8WebmasterBenefitCampaign = "webmaster-benefit-v1"
	defaultOpus8TimeoutMs         = 5_000
	maxOpus8ResponseBytes         = 64 * 1024
)

// Opus8Config holds the integration configuration.
type Opus8Config struct {
	BaseURL   string // HTTPS origin only (no path)
	KeyID     string
	Secret    string
	TimeoutMs int
}

// Opus8BenefitResult is the validated response from Opus8.
type Opus8BenefitResult struct {
	ExternalClaimID string
	OpusUserID      string
	OpusDeviceID    string
	SubscriptionURL string
	ExpiresAt       string
	TrafficBytes    int64
	DurationDays    int
	HWIDRequired    bool
	IPLimit         int
	Created         bool
}

// Opus8Error is returned by the client on API errors.
type Opus8Error struct {
	Message   string
	Code      string
	Retryable bool
	Status    int // 0 if not HTTP
}

func (e *Opus8Error) Error() string { return fmt.Sprintf("opus8(%s): %s", e.Code, e.Message) }

// Opus8Client calls the Opus8 integration API.
type Opus8Client struct {
	cfg    Opus8Config
	client *http.Client
}

// NewOpus8Client creates a validated Opus8Client.
func NewOpus8Client(baseURL, keyID, secret string, timeoutMs int) (*Opus8Client, error) {
	if timeoutMs == 0 {
		timeoutMs = defaultOpus8TimeoutMs
	}
	if timeoutMs < 100 || timeoutMs > 30_000 {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_TIMEOUT_MS must be 100–30000")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_BASE_URL must be a valid HTTPS URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_BASE_URL must not contain credentials")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_BASE_URL must contain only an origin (no path)")
	}
	origin := fmt.Sprintf("https://%s", parsed.Host)

	if !actionRe.MatchString(keyID) || len(keyID) < 3 || len(keyID) > 64 {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_KEY_ID is invalid")
	}
	if len(secret) < 32 || len(secret) > 4_096 {
		return nil, fmt.Errorf("OPUS8_INTEGRATION_SECRET must be 32–4096 characters")
	}

	return &Opus8Client{
		cfg: Opus8Config{
			BaseURL:   origin,
			KeyID:     keyID,
			Secret:    secret,
			TimeoutMs: timeoutMs,
		},
		client: &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}, nil
}

// ClaimWebmasterBenefit calls the Opus8 claim endpoint with idempotency support.
func (c *Opus8Client) ClaimWebmasterBenefit(ctx context.Context, externalClaimID string) (*Opus8BenefitResult, error) {
	if !uuidRe.MatchString(externalClaimID) {
		return nil, fmt.Errorf("invalid external benefit claim ID: %q", externalClaimID)
	}

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	requestID := newUUID()
	rawBody := fmt.Sprintf(`{"externalClaimId":%q,"campaignId":%q}`, externalClaimID, opus8WebmasterBenefitCampaign)

	bodyHash := hashHexStr(rawBody)
	sigMsg := strings.Join([]string{
		"opus8-integration-v1",
		ts,
		requestID,
		"POST",
		opus8WebmasterBenefitPath,
		bodyHash,
	}, "\n")
	sig := hmacHex([]byte(c.cfg.Secret), []byte(sigMsg))

	endpointURL := c.cfg.BaseURL + opus8WebmasterBenefitPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(rawBody))
	if err != nil {
		return nil, &Opus8Error{Message: "failed to build request", Code: "opus8_network_error", Retryable: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-opus8-integration-key-id", c.cfg.KeyID)
	req.Header.Set("x-opus8-integration-timestamp", ts)
	req.Header.Set("x-opus8-integration-request-id", requestID)
	req.Header.Set("x-opus8-integration-signature", sig)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &Opus8Error{Message: "network request failed", Code: "opus8_network_error", Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, normalizeOpus8HTTPError(resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOpus8ResponseBytes+1))
	if err != nil || len(body) > maxOpus8ResponseBytes {
		return nil, &Opus8Error{Message: "response too large", Code: "opus8_invalid_response", Retryable: true}
	}

	return validateOpus8Payload(body, externalClaimID)
}

func normalizeOpus8HTTPError(status int) *Opus8Error {
	switch {
	case status == 400 || status == 413 || status == 422:
		return &Opus8Error{"Opus8 rejected the claim contract", "opus8_contract_rejected", false, status}
	case status == 401:
		return &Opus8Error{"Opus8 authentication failed", "opus8_authentication_failed", false, status}
	case status == 403:
		return &Opus8Error{"Opus8 authorization failed", "opus8_authorization_failed", false, status}
	case status == 404 || status == 405:
		return &Opus8Error{"Opus8 endpoint not found", "opus8_endpoint_not_found", false, status}
	case status == 408:
		return &Opus8Error{"Opus8 request timed out", "opus8_timeout", true, status}
	case status == 409:
		return &Opus8Error{"Opus8 idempotency conflict", "opus8_idempotency_conflict", false, status}
	case status == 429:
		return &Opus8Error{"Opus8 rate limited", "opus8_rate_limited", true, status}
	case status >= 500:
		return &Opus8Error{"Opus8 temporarily unavailable", "opus8_temporarily_unavailable", true, status}
	default:
		return &Opus8Error{"Opus8 unexpected status", "opus8_unexpected_status", false, status}
	}
}

func validateOpus8Payload(body []byte, expectedClaimID string) (*Opus8BenefitResult, error) {
	var p struct {
		ExternalClaimID string `json:"externalClaimId"`
		OpusUserID      string `json:"opusUserId"`
		OpusDeviceID    string `json:"opusDeviceId"`
		SubscriptionURL string `json:"subscriptionUrl"`
		ExpiresAt       string `json:"expiresAt"`
		TrafficBytes    int64  `json:"trafficBytes"`
		DurationDays    int    `json:"durationDays"`
		HWIDRequired    bool   `json:"hwidRequired"`
		IPLimit         int    `json:"ipLimit"`
		Created         bool   `json:"created"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, &Opus8Error{"invalid JSON response", "opus8_invalid_response", true, 0}
	}
	if p.ExternalClaimID != expectedClaimID {
		return nil, &Opus8Error{"mismatched external claim ID", "opus8_invalid_response", true, 0}
	}
	if p.OpusUserID == "" || len(p.OpusUserID) > 128 ||
		p.OpusDeviceID == "" || len(p.OpusDeviceID) > 128 {
		return nil, &Opus8Error{"invalid Opus8 resource identifiers", "opus8_invalid_response", true, 0}
	}
	parsed, err := url.Parse(p.SubscriptionURL)
	if err != nil || parsed.Scheme != "https" || p.SubscriptionURL == "" || len(p.SubscriptionURL) > 2048 {
		return nil, &Opus8Error{"invalid Opus8 subscription URL", "opus8_invalid_response", true, 0}
	}
	if p.ExpiresAt == "" {
		return nil, &Opus8Error{"missing expiresAt in Opus8 response", "opus8_invalid_response", true, 0}
	}
	if _, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
		return nil, &Opus8Error{"invalid expiresAt in Opus8 response", "opus8_invalid_response", true, 0}
	}

	return &Opus8BenefitResult{
		ExternalClaimID: p.ExternalClaimID,
		OpusUserID:      p.OpusUserID,
		OpusDeviceID:    p.OpusDeviceID,
		SubscriptionURL: p.SubscriptionURL,
		ExpiresAt:       p.ExpiresAt,
		TrafficBytes:    p.TrafficBytes,
		DurationDays:    p.DurationDays,
		HWIDRequired:    p.HWIDRequired,
		IPLimit:         p.IPLimit,
		Created:         p.Created,
	}, nil
}
