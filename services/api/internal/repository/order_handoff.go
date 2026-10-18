package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/jackc/pgx/v5"
)

type handoffQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (p *Postgres) CreateOwnedAffiliateOrder(ctx context.Context, input domain.CreateOwnedOrderInput) (*domain.OrderHandoff, bool, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	// Serializing by owner + key lets duplicate requests see the committed order
	// before checking current stock/pricing. The unique constraint is the final guard.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "order-request:"+input.OwnerHash+":"+input.RequestKey); err != nil {
		return nil, false, err
	}
	inputHash := security.HashText(input.ProductSlug + "\n" + input.AffiliateWechatID)
	var orderID, storedHash string
	err = tx.QueryRow(ctx, `SELECT order_id::text, input_hash FROM affiliate_order_requests WHERE owner_hash=$1 AND request_key=$2`, input.OwnerHash, input.RequestKey).Scan(&orderID, &storedHash)
	if err == nil {
		if storedHash != inputHash {
			return nil, false, domain.ErrIdempotencyConflict
		}
		handoff, err := readOwnedOrderHandoff(ctx, tx, input.OwnerHash, orderID)
		return handoff, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	order, err := p.createAffiliateOrderTx(ctx, tx, input.CreateOrderInput)
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_order_requests(owner_hash,request_key,input_hash,order_id) VALUES($1,$2,$3,$4)`, input.OwnerHash, input.RequestKey, inputHash, order.ID); err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_order_support_deliveries(order_id) VALUES($1)`, order.ID); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &domain.OrderHandoff{Order: order, RequestKey: input.RequestKey, Delivery: domain.OrderSupportDelivery{Status: "awaiting_session"}}, true, nil
}

func (p *Postgres) RecoverOwnedAffiliateOrder(ctx context.Context, ownerHash, requestKey string) (*domain.OrderHandoff, error) {
	var orderID string
	err := p.pool.QueryRow(ctx, `SELECT order_id::text FROM affiliate_order_requests WHERE owner_hash=$1 AND request_key=$2`, ownerHash, requestKey).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return readOwnedOrderHandoff(ctx, p.pool, ownerHash, orderID)
}

func (p *Postgres) GetOwnedOrderHandoff(ctx context.Context, ownerHash, orderID string) (*domain.OrderHandoff, error) {
	return readOwnedOrderHandoff(ctx, p.pool, ownerHash, orderID)
}

func readOwnedOrderHandoff(ctx context.Context, q handoffQuerier, ownerHash, orderID string) (*domain.OrderHandoff, error) {
	h := domain.OrderHandoff{Order: &domain.AffiliateOrder{}}
	o, d := h.Order, &h.Delivery
	err := q.QueryRow(ctx, `SELECT o.id, o.order_code, o.affiliate_id, COALESCE(o.product_id::text,''), o.product_title,
		o.price_cents,o.commission_cents,o.base_commission_cents,o.markup_commission_cents,o.currency,o.order_status,o.commission_status,o.created_at,o.updated_at,
		r.request_key::text,d.status,COALESCE(d.binding_id,''),COALESCE(d.message_id,0),COALESCE(d.conversation_id,0),d.attempts,d.error_code,d.lease_until
		FROM affiliate_order_requests r JOIN affiliate_orders o ON o.id=r.order_id JOIN affiliate_order_support_deliveries d ON d.order_id=o.id
		WHERE r.owner_hash=$1 AND r.order_id=$2`, ownerHash, orderID).Scan(
		&o.ID, &o.OrderCode, &o.AffiliateID, &o.ProductID, &o.ProductTitle, &o.PriceCents, &o.CommissionCents, &o.BaseCommissionCents, &o.MarkupCommissionCents, &o.Currency, &o.OrderStatus, &o.CommissionStatus, &o.CreatedAt, &o.UpdatedAt,
		&h.RequestKey, &d.Status, &d.BindingID, &d.MessageID, &d.ConversationID, &d.Attempts, &d.ErrorCode, &d.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.CustomerPriceCents, o.Status = o.PriceCents, o.OrderStatus
	return &h, nil
}

func (p *Postgres) ClaimOrderSupportDelivery(ctx context.Context, ownerHash, orderID, bindingID string) (*domain.OrderHandoff, bool, error) {
	if bindingID == "" || len(bindingID) > 256 {
		return nil, false, domain.ErrInvalidState
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	var storedBinding, status string
	var busy bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(d.binding_id,''),d.status,COALESCE(d.lease_until > now(),false)
		FROM affiliate_order_support_deliveries d JOIN affiliate_order_requests r ON r.order_id=d.order_id
		WHERE r.owner_hash=$1 AND d.order_id=$2 FOR UPDATE OF d`, ownerHash, orderID).Scan(&storedBinding, &status, &busy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, domain.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if storedBinding != "" && storedBinding != bindingID {
		return nil, false, domain.ErrSupportBindingMismatch
	}
	claimed := status != "sent" && !busy
	if claimed {
		// Never hold a database transaction open across a network request. A stale
		// lease can be replayed only with the receiver's verified idempotency key.
		_, err = tx.Exec(ctx, `UPDATE affiliate_order_support_deliveries SET binding_id=$2,status='sending',attempts=attempts+1,error_code='',lease_until=now()+interval '30 seconds',updated_at=now() WHERE order_id=$1`, orderID, bindingID)
		if err != nil {
			return nil, false, err
		}
	}
	h, err := readOwnedOrderHandoff(ctx, tx, ownerHash, orderID)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return h, claimed, nil
}

func (p *Postgres) FinishOrderSupportDelivery(ctx context.Context, ownerHash, orderID, bindingID string, attempt int, status, errorCode string, messageID, conversationID int64) (*domain.OrderHandoff, error) {
	if status != "sent" && status != "reconciling" && status != "retry_wait" && status != "action_required" {
		return nil, domain.ErrInvalidState
	}
	if (status == "sent") != (messageID > 0 && conversationID > 0) {
		return nil, domain.ErrInvalidState
	}
	_, err := p.pool.Exec(ctx, `UPDATE affiliate_order_support_deliveries d SET status=$5,error_code=$6,message_id=NULLIF($7,0),conversation_id=NULLIF($8,0),lease_until=NULL,updated_at=now()
		FROM affiliate_order_requests r WHERE r.order_id=d.order_id AND r.owner_hash=$1 AND d.order_id=$2 AND d.binding_id=$3 AND ((d.attempts=$4 AND d.status='sending') OR ($5='sent' AND d.status <> 'sent'))`, ownerHash, orderID, bindingID, attempt, status, errorCode, messageID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("save order delivery result: %w", err)
	}
	return readOwnedOrderHandoff(ctx, p.pool, ownerHash, orderID)
}

func (p *Postgres) SetOrderSupportWaiting(ctx context.Context, ownerHash, orderID, status, errorCode string) (*domain.OrderHandoff, error) {
	if status != "retry_wait" && status != "action_required" && status != "awaiting_session" {
		return nil, domain.ErrInvalidState
	}
	_, err := p.pool.Exec(ctx, `UPDATE affiliate_order_support_deliveries d SET status=$3,error_code=$4,updated_at=now()
		FROM affiliate_order_requests r WHERE r.order_id=d.order_id AND r.owner_hash=$1 AND d.order_id=$2 AND d.status NOT IN ('sent','sending','reconciling')`, ownerHash, orderID, status, errorCode)
	if err != nil {
		return nil, err
	}
	return readOwnedOrderHandoff(ctx, p.pool, ownerHash, orderID)
}
