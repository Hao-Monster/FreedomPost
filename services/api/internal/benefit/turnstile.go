// Package benefit implements Cloudflare Turnstile verification, subscription
// link encryption, and Opus8 integration for the Webmaster Benefit feature.
package benefit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Turnstile constants — aligned with TS implementation.
const (
	turnstileSiteverifyURL    = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	defaultTurnstileAction    = "webmaster_benefit_claim"
	maxTurnstileResponseBytes = 16 * 1024
	maxChallengeAge           = 5 * time.Minute
	clockSkew                 = 60 * time.Second
	maxAttempts               = 2
)

var hostnameRe = regexp.MustCompile(`^[a-z0-9.-]+$`)
var actionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// TurnstileConfig holds validated Turnstile configuration.
type TurnstileConfig struct {
	SecretKey        string
	ExpectedHostname string
	ExpectedAction   string
	TimeoutMs        int
}

// TurnstileResult is the verification outcome.
type TurnstileResult struct {
	Valid     bool
	Code      string // non-empty when Valid == false
	Retryable bool
}

// TurnstileVerifier verifies Cloudflare Turnstile tokens.
type TurnstileVerifier struct {
	cfg    TurnstileConfig
	client *http.Client
}

// NewTurnstileVerifier creates a validated TurnstileVerifier.
func NewTurnstileVerifier(secretKey, expectedHostname, expectedAction string, timeoutMs int) (*TurnstileVerifier, error) {
	if len(secretKey) < 20 || len(secretKey) > 256 {
		return nil, fmt.Errorf("TURNSTILE_SECRET_KEY is invalid")
	}
	hostname := strings.TrimSpace(strings.ToLower(expectedHostname))
	if hostname == "" || len(hostname) > 253 ||
		strings.Contains(hostname, "://") || strings.Contains(hostname, "/") ||
		!hostnameRe.MatchString(hostname) {
		return nil, fmt.Errorf("TURNSTILE_EXPECTED_HOSTNAME is invalid: %q", expectedHostname)
	}
	action := expectedAction
	if action == "" {
		action = defaultTurnstileAction
	}
	if !actionRe.MatchString(action) {
		return nil, fmt.Errorf("TURNSTILE_EXPECTED_ACTION is invalid: %q", action)
	}
	if timeoutMs == 0 {
		timeoutMs = 3_000
	}
	if timeoutMs < 100 || timeoutMs > 10_000 {
		return nil, fmt.Errorf("TURNSTILE_TIMEOUT_MS must be 100–10000, got %d", timeoutMs)
	}

	return &TurnstileVerifier{
		cfg: TurnstileConfig{
			SecretKey:        secretKey,
			ExpectedHostname: hostname,
			ExpectedAction:   action,
			TimeoutMs:        timeoutMs,
		},
		client: &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}, nil
}

// Verify verifies a Turnstile token. remoteIP may be empty.
func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) TurnstileResult {
	if len(token) < 1 || len(token) > 2_048 {
		return rejected("turnstile_rejected", false)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result := v.attempt(ctx, token, remoteIP)
		switch result {
		case "ok":
			return TurnstileResult{Valid: true}
		case "temporary_failure":
			if attempt < maxAttempts {
				continue
			}
			return rejected("turnstile_unavailable", true)
		case "rejected_http":
			return rejected("turnstile_invalid_response", false)
		case "rejected":
			return rejected("turnstile_rejected", false)
		case "context_mismatch":
			return rejected("turnstile_context_mismatch", false)
		case "internal_retry":
			if attempt < maxAttempts {
				continue
			}
			return rejected("turnstile_unavailable", true)
		}
	}
	return rejected("turnstile_unavailable", true)
}

func (v *TurnstileVerifier) attempt(ctx context.Context, token, remoteIP string) string {
	form := url.Values{
		"secret":          {v.cfg.SecretKey},
		"response":        {token},
		"idempotency_key": {newUUID()},
	}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileSiteverifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "temporary_failure"
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "temporary_failure"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch {
		case resp.StatusCode == 408, resp.StatusCode == 429, resp.StatusCode >= 500:
			return "temporary_failure"
		default:
			return "rejected_http"
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTurnstileResponseBytes+1))
	if err != nil || len(body) > maxTurnstileResponseBytes {
		return "temporary_failure"
	}

	var payload struct {
		Success     bool     `json:"success"`
		ChallengeTS string   `json:"challenge_ts"`
		Hostname    string   `json:"hostname"`
		Action      string   `json:"action"`
		ErrorCodes  []string `json:"error-codes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "internal_retry"
	}
	if !payload.Success {
		for _, code := range payload.ErrorCodes {
			if code == "internal-error" {
				return "internal_retry"
			}
		}
		return "rejected"
	}

	// Validate context: hostname, action, challenge age
	challengeTime, err := time.Parse(time.RFC3339, payload.ChallengeTS)
	if err != nil {
		return "context_mismatch"
	}
	now := time.Now().UTC()
	challengeAge := now.Sub(challengeTime.UTC())
	if payload.Hostname != v.cfg.ExpectedHostname ||
		payload.Action != v.cfg.ExpectedAction ||
		challengeAge > maxChallengeAge+clockSkew ||
		challengeTime.UTC().After(now.Add(clockSkew)) {
		return "context_mismatch"
	}
	return "ok"
}

func rejected(code string, retryable bool) TurnstileResult {
	return TurnstileResult{Valid: false, Code: code, Retryable: retryable}
}
