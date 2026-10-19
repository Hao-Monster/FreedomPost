package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrIdempotencyConflict    = errors.New("order idempotency conflict")
	ErrSupportBindingMismatch = errors.New("support binding mismatch")
)

// OrderSupportDelivery is independent of the financial order state. BindingID
// is an opaque identity verified by Chatwoot, never a browser-supplied contact ID.
type OrderSupportDelivery struct {
	Status         string     `json:"status"`
	BindingID      string     `json:"bindingId,omitempty"`
	MessageID      int64      `json:"messageId,omitempty"`
	ConversationID int64      `json:"conversationId,omitempty"`
	Attempts       int        `json:"attempts"`
	ErrorCode      string     `json:"errorCode,omitempty"`
	LeaseUntil     *time.Time `json:"-"`
}

type OrderHandoff struct {
	Order      *AffiliateOrder      `json:"order"`
	Delivery   OrderSupportDelivery `json:"supportDelivery"`
	RequestKey string               `json:"-"`
}

type CreateOwnedOrderInput struct {
	CreateOrderInput
	OwnerHash  string
	RequestKey string
}

type OrderHandoffRepository interface {
	LookupOwnedAffiliateOrder(context.Context, CreateOwnedOrderInput) (*OrderHandoff, error)
	CreateOwnedAffiliateOrder(context.Context, CreateOwnedOrderInput) (*OrderHandoff, bool, error)
	RecoverOwnedAffiliateOrder(ctx context.Context, ownerHash, requestKey string) (*OrderHandoff, error)
	GetOwnedOrderHandoff(ctx context.Context, ownerHash, orderID string) (*OrderHandoff, error)
	ClaimOrderSupportDelivery(ctx context.Context, ownerHash, orderID, bindingID string) (*OrderHandoff, bool, error)
	FinishOrderSupportDelivery(ctx context.Context, ownerHash, orderID, bindingID string, attempt int, status, errorCode string, messageID, conversationID int64) (*OrderHandoff, error)
	SetOrderSupportWaiting(ctx context.Context, ownerHash, orderID, status, errorCode string) (*OrderHandoff, error)
}
