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
)

type Client struct {
	base, token        string
	accountID, inboxID int64
	http               *http.Client
}

func New(base, token string, accountID, inboxID int64, timeout time.Duration) (*Client, error) {
	if base == "" || token == "" || accountID < 1 || inboxID < 1 {
		return nil, nil
	}
	u, e := url.Parse(strings.TrimRight(base, "/"))
	if e != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("invalid chatwoot base url")
	}
	return &Client{u.String(), token, accountID, inboxID, &http.Client{Timeout: timeout}}, nil
}
func (c *Client) req(ctx context.Context, m, p string, b, out any) error {
	var rd io.Reader
	if b != nil {
		x, e := json.Marshal(b)
		if e != nil {
			return e
		}
		rd = bytes.NewReader(x)
	}
	q, e := http.NewRequestWithContext(ctx, m, c.base+p, rd)
	if e != nil {
		return e
	}
	q.Header.Set("api_access_token", c.token)
	if b != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	r, e := c.http.Do(q)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	d, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return fmt.Errorf("chatwoot status %d", r.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(d, out)
	}
	return nil
}
func (c *Client) SendOrder(ctx context.Context, visitor, content string) error {
	var x struct {
		ID int64 `json:"id"`
	}
	var search struct {
		Payload []struct {
			ID int64 `json:"id"`
		} `json:"payload"`
	}
	searchPath := fmt.Sprintf("/api/v1/accounts/%d/contacts/search?q=%s", c.accountID, url.QueryEscape(visitor))
	_ = c.req(ctx, http.MethodGet, searchPath, nil, &search)
	if len(search.Payload) > 0 {
		x.ID = search.Payload[0].ID
	}
	var e error
	if x.ID == 0 {
		e = c.req(ctx, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts", c.accountID), map[string]any{"inbox_id": c.inboxID, "name": "FreedomPost访客", "identifier": visitor}, &x)
	}
	if e != nil {
		return e
	}
	if x.ID == 0 {
		return fmt.Errorf("missing contact id")
	}
	var y struct {
		ID int64 `json:"id"`
	}
	return c.req(ctx, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/conversations", c.accountID), map[string]any{"source_id": visitor, "contact_id": x.ID, "inbox_id": c.inboxID, "status": "open", "message": map[string]any{"content": content, "message_type": "incoming", "private": false}}, &y)
}
