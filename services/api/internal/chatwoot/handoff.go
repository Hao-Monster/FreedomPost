package chatwoot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type HandoffCapability struct {
	Version          int    `json:"version"`
	BindingID        string `json:"binding_id"`
	IdempotentOrders bool   `json:"idempotent_orders"`
}

type HandoffReceipt struct {
	Version        int    `json:"version"`
	BindingID      string `json:"binding_id"`
	ConversationID int64  `json:"conversation_id"`
	RequestKey     string `json:"request_key"`
	MessageID      int64  `json:"-"`
}

// HandoffError contains only a fixed public code, never response bodies,
// request URLs, signed tokens or transport diagnostics.
type HandoffError struct {
	Code           string
	UnknownOutcome bool
	Retryable      bool
}

func (e *HandoffError) Error() string { return e.Code }

func (c *Client) HandoffCapability(ctx context.Context, token string) (*HandoffCapability, error) {
	body, err := c.handoffRequest(ctx, http.MethodGet, "/api/v1/widget/freedompost_handoff", token, nil)
	if err != nil {
		return nil, err
	}
	var capability HandoffCapability
	if json.Unmarshal(body, &capability) != nil || capability.Version != 1 || !capability.IdempotentOrders || !validBindingID(capability.BindingID) {
		return nil, &HandoffError{Code: "SUPPORT_CAPABILITY_UNAVAILABLE"}
	}
	return &capability, nil
}

// SendOrderHandoff is called only after a verified capability and immutable
// identity binding. The stable order UUID is the receiver's unique request key.
func (c *Client) SendOrderHandoff(ctx context.Context, token, orderID, bindingID, content, referer string) (*HandoffReceipt, error) {
	if uuid.Validate(orderID) != nil || !validBindingID(bindingID) || strings.TrimSpace(content) == "" || len(content) > 16*1024 {
		return nil, &HandoffError{Code: "SUPPORT_INVALID_REQUEST"}
	}
	if len(referer) > 2048 {
		referer = ""
	}
	body, err := json.Marshal(map[string]any{"message": map[string]any{
		"content": content, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "referer_url": referer, "freedompost_delivery_key": orderID,
	}})
	if err != nil {
		return nil, err
	}
	response, err := c.handoffRequest(ctx, http.MethodPost, "/api/v1/widget/messages", token, body)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID          int64          `json:"id"`
		Content     string         `json:"content"`
		MessageType *int           `json:"message_type"`
		Private     *bool          `json:"private"`
		Delivery    HandoffReceipt `json:"freedompost_delivery"`
	}
	if json.Unmarshal(response, &created) != nil || created.ID <= 0 || created.Content != content || created.MessageType == nil || *created.MessageType != 0 || created.Private == nil || *created.Private || created.Delivery.Version != 1 || created.Delivery.BindingID != bindingID || created.Delivery.RequestKey != orderID || created.Delivery.ConversationID <= 0 {
		return nil, &HandoffError{Code: "SUPPORT_RECEIPT_INVALID", UnknownOutcome: true, Retryable: true}
	}
	created.Delivery.MessageID = created.ID
	return &created.Delivery, nil
}

func validBindingID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func (c *Client) handoffRequest(ctx context.Context, method, path, token string, body []byte) ([]byte, error) {
	if c == nil || validateToken(token, 4096) != nil {
		return nil, &HandoffError{Code: "SUPPORT_SESSION_REQUIRED"}
	}
	endpoint, err := url.Parse(c.base + path)
	if err != nil {
		return nil, &HandoffError{Code: "SUPPORT_CONFIGURATION_INVALID"}
	}
	query := endpoint.Query()
	query.Set("website_token", c.websiteToken)
	query.Set("locale", "zh_CN")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &HandoffError{Code: "SUPPORT_INVALID_REQUEST"}
	}
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, &HandoffError{Code: "SUPPORT_UNREACHABLE", UnknownOutcome: method == http.MethodPost, Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		code := fmt.Sprintf("SUPPORT_HTTP_%d", response.StatusCode)
		return nil, &HandoffError{Code: code, UnknownOutcome: method == http.MethodPost && response.StatusCode >= 500, Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	const maxResponseBytes = 1 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responseBody) > maxResponseBytes {
		return nil, &HandoffError{Code: "SUPPORT_RESPONSE_INVALID", UnknownOutcome: method == http.MethodPost, Retryable: true}
	}
	return responseBody, nil
}
