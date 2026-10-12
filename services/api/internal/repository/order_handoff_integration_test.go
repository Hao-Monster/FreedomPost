package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOwnedOrderDoesNotAcquireSecondPoolConnectionInsideTransaction(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	cfg := repo.pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	limited := &Postgres{pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err = limited.CreateOwnedAffiliateOrder(ctx, input); err != nil {
		t.Fatalf("order needs only its transaction connection: %v", err)
	}
}

func orderHandoffFixture(t *testing.T) (*Postgres, domain.CreateOwnedOrderInput) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; real PostgreSQL integration NOT RUN")
	}
	ctx := context.Background()
	repo, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)
	affiliate, err := repo.CreateAffiliate(ctx, fmt.Sprintf("QAOrder%d", time.Now().UnixNano()), "unused-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	product, err := repo.CreateProduct(ctx, domain.ProductInput{Title: "QA order handoff", PriceCents: 1200, CommissionCents: 100, Currency: "CNY", Stock: 20, Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, sql := range []string{
			`DELETE FROM affiliate_order_support_deliveries WHERE order_id IN (SELECT id FROM affiliate_orders WHERE affiliate_id=$1)`,
			`DELETE FROM affiliate_order_requests WHERE order_id IN (SELECT id FROM affiliate_orders WHERE affiliate_id=$1)`,
			`DELETE FROM affiliate_orders WHERE affiliate_id=$1`,
			`DELETE FROM affiliate_product_markups WHERE affiliate_id=$1`,
			`DELETE FROM affiliates WHERE id=$1`,
		} {
			if _, err := repo.pool.Exec(cleanupCtx, sql, affiliate.ID); err != nil {
				t.Errorf("cleanup QA fixture: %v", err)
			}
		}
		if _, err := repo.pool.Exec(cleanupCtx, `DELETE FROM products WHERE id=$1`, product.ID); err != nil {
			t.Errorf("cleanup QA product: %v", err)
		}
	})
	return repo, domain.CreateOwnedOrderInput{CreateOrderInput: domain.CreateOrderInput{ProductSlug: product.Slug, AffiliateWechatID: affiliate.WechatID}, OwnerHash: security.HashText(uuid.NewString()), RequestKey: uuid.NewString()}
}

func TestOwnedOrderConcurrentCreationAndResponseLossRecovery(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const n = 12
	type result struct {
		h       *domain.OrderHandoff
		created bool
		err     error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, created, err := repo.CreateOwnedAffiliateOrder(ctx, input)
			results <- result{h, created, err}
		}()
	}
	wg.Wait()
	close(results)
	createdCount, orderID := 0, ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
		if orderID == "" {
			orderID = result.h.Order.ID
		}
		if result.h.Order.ID != orderID || result.h.Delivery.Status != "awaiting_session" {
			t.Fatalf("different logical order after concurrent retries: %+v", result.h)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d orders, want 1", createdCount)
	}
	var orders, requests, deliveries int
	err := repo.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM affiliate_orders WHERE id=$1),(SELECT count(*) FROM affiliate_order_requests WHERE order_id=$1),(SELECT count(*) FROM affiliate_order_support_deliveries WHERE order_id=$1)`, orderID).Scan(&orders, &requests, &deliveries)
	if err != nil || orders != 1 || requests != 1 || deliveries != 1 {
		t.Fatalf("transactional cardinality %d/%d/%d: %v", orders, requests, deliveries, err)
	}
	// Simulate losing the entire creation response: only persisted owner+key remain.
	recovered, err := repo.RecoverOwnedAffiliateOrder(ctx, input.OwnerHash, input.RequestKey)
	if err != nil || recovered.Order.ID != orderID {
		t.Fatalf("recover response loss: %+v %v", recovered, err)
	}
	if _, err = repo.RecoverOwnedAffiliateOrder(ctx, security.HashText("another-owner"), input.RequestKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner recovered order: %v", err)
	}
	if _, err = repo.GetOwnedOrderHandoff(ctx, security.HashText("another-owner"), orderID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner read order: %v", err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE products SET stock=0,price_cents=9999 WHERE id=$1`, recovered.Order.ProductID); err != nil {
		t.Fatal(err)
	}
	replay, created, err := repo.CreateOwnedAffiliateOrder(ctx, input)
	if err != nil || created || replay.Order.ID != orderID || replay.Order.PriceCents != 1200 {
		t.Fatalf("retry must return original snapshot after stock/price change: %+v %v", replay, err)
	}
	conflicting := input
	conflicting.ProductSlug = "different-product"
	if _, _, err = repo.CreateOwnedAffiliateOrder(ctx, conflicting); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("changed payload should conflict before pricing: %v", err)
	}
}

func TestOwnedOrderSeparateOperationsAndRollback(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	first, _, err := repo.CreateOwnedAffiliateOrder(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.RequestKey = uuid.NewString()
	second, created, err := repo.CreateOwnedAffiliateOrder(ctx, secondInput)
	if err != nil || !created || first.Order.ID == second.Order.ID {
		t.Fatalf("distinct operation collapsed: %v", err)
	}
	thirdInput := input
	thirdInput.OwnerHash = security.HashText("other-owner")
	third, created, err := repo.CreateOwnedAffiliateOrder(ctx, thirdInput)
	if err != nil || !created || first.Order.ID == third.Order.ID {
		t.Fatalf("request key must be scoped to owner: %v", err)
	}
	bad := input
	bad.RequestKey = uuid.NewString()
	bad.AffiliateWechatID = "MissingAffiliate"
	if _, _, err = repo.CreateOwnedAffiliateOrder(ctx, bad); !errors.Is(err, domain.ErrInvalidRecommender) {
		t.Fatalf("expected invalid affiliate: %v", err)
	}
	if _, err = repo.RecoverOwnedAffiliateOrder(ctx, bad.OwnerHash, bad.RequestKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed creation leaked persisted request: %v", err)
	}
}

func TestOrderSupportDeliveryBindingLeaseAndReceiptCAS(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	h, _, err := repo.CreateOwnedAffiliateOrder(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	const binding = "verified-chatwoot-binding"
	const n = 8
	type claimResult struct {
		h       *domain.OrderHandoff
		claimed bool
		err     error
	}
	results := make(chan claimResult, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, claimed, err := repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding)
			results <- claimResult{v, claimed, err}
		}()
	}
	wg.Wait()
	close(results)
	claims := 0
	for v := range results {
		if v.err != nil {
			t.Fatal(v.err)
		}
		if v.claimed {
			claims++
		}
		if v.h.Delivery.Attempts != 1 {
			t.Fatalf("attempts=%d", v.h.Delivery.Attempts)
		}
	}
	if claims != 1 {
		t.Fatalf("concurrent sending leases=%d", claims)
	}
	if _, _, err = repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, "another-binding"); !errors.Is(err, domain.ErrSupportBindingMismatch) {
		t.Fatalf("binding changed: %v", err)
	}
	if _, _, err = repo.ClaimOrderSupportDelivery(ctx, security.HashText("stranger"), h.Order.ID, binding); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner claim: %v", err)
	}
	unknown, err := repo.FinishOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding, 1, "reconciling", "SUPPORT_UNREACHABLE", 0, 0)
	if err != nil || unknown.Delivery.Status != "reconciling" {
		t.Fatalf("unknown receipt state lost: %v", err)
	}
	retry, claimed, err := repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding)
	if err != nil || !claimed || retry.Delivery.Attempts != 2 {
		t.Fatalf("same binding reconciliation unavailable: %v", err)
	}
	late, err := repo.FinishOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding, 1, "action_required", "LATE_FAILURE", 0, 0)
	if err != nil || late.Delivery.Status != "sending" || late.Delivery.Attempts != 2 {
		t.Fatalf("stale attempt overwrote newer attempt: %v", err)
	}
	sent, err := repo.FinishOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding, 2, "sent", "", 71, 19)
	if err != nil || sent.Delivery.MessageID != 71 || sent.Delivery.ConversationID != 19 || sent.Delivery.Status != "sent" {
		t.Fatalf("receipt not persisted: %+v %v", sent, err)
	}
	again, claimed, err := repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, binding)
	if err != nil || claimed || again.Delivery.MessageID != 71 {
		t.Fatalf("sent order reacquired: %v", err)
	}
	unchanged, err := repo.SetOrderSupportWaiting(ctx, input.OwnerHash, h.Order.ID, "awaiting_session", "MISSING_COOKIE")
	if err != nil || unchanged.Delivery.Status != "sent" {
		t.Fatalf("transient browser state erased sent receipt: %v", err)
	}
}

func TestOrderSupportDeliveryStaleLeaseRecoveryAndConstraints(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	h, _, err := repo.CreateOwnedAffiliateOrder(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, "verified-binding"); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE affiliate_order_support_deliveries SET lease_until=now()-interval '1 second' WHERE order_id=$1`, h.Order.ID); err != nil {
		t.Fatal(err)
	}
	again, claimed, err := repo.ClaimOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, "verified-binding")
	if err != nil || !claimed || again.Delivery.Attempts != 2 {
		t.Fatalf("abandoned lease cannot be recovered: %v", err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE affiliate_order_support_deliveries SET status='sent',message_id=NULL,conversation_id=NULL WHERE order_id=$1`, h.Order.ID); err == nil {
		t.Fatal("database allowed false sent without receipt")
	}
	if _, err = repo.FinishOrderSupportDelivery(ctx, input.OwnerHash, h.Order.ID, "verified-binding", 2, "sent", "", 0, 0); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("missing receipt accepted: %v", err)
	}
}
