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

type contactInbox struct {
	SourceID string `json:"source_id"`
	Inbox    struct {
		ID int64 `json:"id"`
	} `json:"inbox"`
}

type contact struct {
	ID             int64          `json:"id"`
	Identifier     string         `json:"identifier"`
	SourceID       string         `json:"source_id"`
	ContactInboxes []contactInbox `json:"contact_inboxes"`
}

type contactsResponse struct {
	Payload []contact `json:"payload"`
}

// UnmarshalJSON accepts both response shapes used by Chatwoot releases:
// search returns payload as an array, while create-contact may return an
// object containing contact and contact_inbox.
func (r *contactsResponse) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Payload        json.RawMessage `json:"payload"`
		ID             int64           `json:"id"`
		Identifier     string          `json:"identifier"`
		SourceID       string          `json:"source_id"`
		ContactInboxes []contactInbox  `json:"contact_inboxes"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}

	r.Payload = nil
	if len(envelope.Payload) > 0 && string(envelope.Payload) != "null" {
		contacts, err := decodeContactsPayload(envelope.Payload)
		if err != nil {
			return err
		}
		r.Payload = append(r.Payload, contacts...)
	}
	if len(r.Payload) == 0 && envelope.ID != 0 {
		r.Payload = []contact{{
			ID:             envelope.ID,
			Identifier:     envelope.Identifier,
			SourceID:       envelope.SourceID,
			ContactInboxes: envelope.ContactInboxes,
		}}
	}
	return nil
}

func decodeContactsPayload(data []byte) ([]contact, error) {
	var list []contact
	if err := json.Unmarshal(data, &list); err == nil {
		return list, nil
	}

	var object struct {
		Contact      *contact      `json:"contact"`
		ContactInbox *contactInbox `json:"contact_inbox"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object.Contact != nil {
		value := *object.Contact
		if object.ContactInbox != nil {
			value.ContactInboxes = append(value.ContactInboxes, *object.ContactInbox)
		}
		return []contact{value}, nil
	}

	var value contact
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.ID == 0 {
		return nil, fmt.Errorf("chatwoot contact response did not contain a contact")
	}
	return []contact{value}, nil
}

func contactSourceID(value contact, inboxID int64) string {
	if value.SourceID != "" {
		return value.SourceID
	}
	var fallback string
	for _, inbox := range value.ContactInboxes {
		if inbox.SourceID == "" {
			continue
		}
		if inbox.Inbox.ID == inboxID {
			return inbox.SourceID
		}
		if inbox.Inbox.ID == 0 && fallback == "" {
			fallback = inbox.SourceID
		}
	}
	return fallback
}

func New(base, token string, accountID, inboxID int64, timeout time.Duration) (*Client, error) {
	if base == "" && token == "" && accountID == 0 && inboxID == 0 {
		return nil, nil
	}
	if base == "" || token == "" || accountID < 1 || inboxID < 1 {
		return nil, fmt.Errorf("incomplete chatwoot configuration")
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
	searchPath := fmt.Sprintf("/api/v1/accounts/%d/contacts/search?q=%s", c.accountID, url.QueryEscape(visitor))
	var search contactsResponse
	if err := c.req(ctx, http.MethodGet, searchPath, nil, &search); err != nil {
		return err
	}
	var selected contact
	if len(search.Payload) > 0 {
		selected = search.Payload[0]
	}
	if selected.ID == 0 {
		var created contactsResponse
		if err := c.req(ctx, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts", c.accountID), map[string]any{"inbox_id": c.inboxID, "name": "FreedomPost访客", "identifier": visitor}, &created); err != nil {
			return err
		}
		if len(created.Payload) == 0 {
			return fmt.Errorf("chatwoot contact response did not contain a payload")
		}
		selected = created.Payload[0]
	}
	sourceID := contactSourceID(selected, c.inboxID)
	if selected.ID == 0 || sourceID == "" {
		return fmt.Errorf("missing chatwoot contact identity")
	}
	var y struct {
		ID int64 `json:"id"`
	}
	return c.req(ctx, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/conversations", c.accountID), map[string]any{"source_id": sourceID, "contact_id": selected.ID, "inbox_id": c.inboxID, "status": "open", "message": map[string]any{"content": content, "message_type": "incoming", "content_type": "text", "private": false}}, &y)
}
