// Package paidaccess provides an HTTP client for calling the paid-access
// internal service. All requests are signed with HMAC-SHA256 matching the
// validInternalRequest() verification in paid-access/internal/httpapi/handler.go.
package paidaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

// Client is a signed HTTP client for the paid-access service.
type Client struct {
	http    *http.Client
	baseURL string
	secret  string
}

// NewClient creates a new paid-access client.
func NewClient(baseURL, secret string) *Client {
	return &Client{
		baseURL: baseURL,
		secret:  secret,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ─── Admin API (proxied through main API) ────────────────────────────────────

// ListReaderAccounts returns all reader accounts via paid-access admin endpoint.
func (c *Client) ListReaderAccounts(ctx context.Context, adminUsername string) (json.RawMessage, error) {
	return c.get(ctx, "/internal/accounts", adminUsername)
}

// ResetReaderPassword resets a reader account password.
func (c *Client) ResetReaderPassword(ctx context.Context, accountID, newPasswordHash, adminUsername string) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]string{"passwordHash": newPasswordHash})
	return c.post(ctx, fmt.Sprintf("/internal/accounts/%s/reset-password", accountID), body, adminUsername)
}

// ListArticleOrders returns paid article orders.
func (c *Client) ListArticleOrders(ctx context.Context, adminUsername string) (json.RawMessage, error) {
	return c.get(ctx, "/internal/orders", adminUsername)
}

// UpdateArticleOrderStatus updates an article order status.
func (c *Client) UpdateArticleOrderStatus(ctx context.Context, orderID, status, adminUsername string) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]string{"status": status})
	return c.patch(ctx, fmt.Sprintf("/internal/orders/%s", orderID), body, adminUsername)
}

// ─── Internal request transport ──────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path, actor string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, path, nil, actor)
}

func (c *Client) post(ctx context.Context, path string, body []byte, actor string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, path, body, actor)
}

func (c *Client) patch(ctx context.Context, path string, body []byte, actor string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPatch, path, body, actor)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, actor string) (json.RawMessage, error) {
	if body == nil {
		body = []byte{}
	}

	ts, sig := security.SignRequest(c.secret, method, path, body)

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build paid-access request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FreedomPost-Timestamp", ts)
	req.Header.Set("X-FreedomPost-Signature", sig)
	if actor != "" {
		req.Header.Set("X-FreedomPost-Admin", actor)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paid-access request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read paid-access response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("paid-access server error: status %d", resp.StatusCode)
	}

	return json.RawMessage(data), nil
}

// ─── Public API (forwarded from /api/reader/*) ────────────────────────────────

// ForwardRequest forwards a public reader request to paid-access unchanged.
// Used for /api/reader/* endpoints that the main API proxies verbatim.
func (c *Client) ForwardRequest(ctx context.Context, method, path string, body []byte, cookieHeader string) (*http.Response, error) {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	return c.http.Do(req)
}
