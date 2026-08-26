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
)

func TestCreateAffiliateOrderUsesStoredPricing(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(repo.Close)

	suffix := time.Now().UnixNano()
	wechatID := fmt.Sprintf("SecTest%d", suffix)
	affiliate, err := repo.CreateAffiliate(ctx, wechatID, "unused-test-hash")
	if err != nil {
		t.Fatalf("create affiliate: %v", err)
	}
	product, err := repo.CreateProduct(ctx, domain.ProductInput{
		Title: "security-order-test", PriceCents: 9900, CommissionCents: 500,
		Currency: "CNY", Stock: 5, Status: "published",
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = repo.pool.Exec(cleanupCtx, "DELETE FROM affiliate_orders WHERE affiliate_id = $1", affiliate.ID)
		_, _ = repo.pool.Exec(cleanupCtx, "DELETE FROM affiliate_product_markups WHERE affiliate_id = $1", affiliate.ID)
		_, _ = repo.pool.Exec(cleanupCtx, "DELETE FROM products WHERE id = $1", product.ID)
		_, _ = repo.pool.Exec(cleanupCtx, "DELETE FROM affiliates WHERE id = $1", affiliate.ID)
	})

	if err := repo.SetAffiliateMarkup(ctx, affiliate.ID, []string{product.ID}, 10); err != nil {
		t.Fatalf("set stored markup: %v", err)
	}
	order, err := repo.CreateAffiliateOrder(ctx, domain.CreateOrderInput{
		AffiliateWechatID: wechatID,
		ProductSlug:       product.Slug,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.PriceCents != 10890 || order.BaseCommissionCents != 500 || order.MarkupCommissionCents != 990 || order.CommissionCents != 1490 {
		t.Fatalf("order did not use authoritative stored pricing: %+v", order)
	}

	const concurrentClicks = 8
	var waitGroup sync.WaitGroup
	uniqueResults := make(chan bool, concurrentClicks)
	errorsFound := make(chan error, concurrentClicks)
	for range concurrentClicks {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			accepted, unique, clickErr := repo.RecordAffiliateClick(ctx, wechatID, "same-visitor", "/market/")
			if clickErr != nil {
				errorsFound <- clickErr
				return
			}
			if !accepted {
				errorsFound <- errors.New("active affiliate click was not accepted")
				return
			}
			uniqueResults <- unique
		}()
	}
	waitGroup.Wait()
	close(errorsFound)
	close(uniqueResults)
	for clickErr := range errorsFound {
		t.Errorf("record concurrent click: %v", clickErr)
	}
	uniqueCount := 0
	for unique := range uniqueResults {
		if unique {
			uniqueCount++
		}
	}
	if uniqueCount != 1 {
		t.Fatalf("concurrent duplicate clicks produced %d unique records, want 1", uniqueCount)
	}

	if _, err := repo.pool.Exec(ctx, "UPDATE products SET stock = 0 WHERE id = $1", product.ID); err != nil {
		t.Fatalf("mark product sold out: %v", err)
	}
	_, err = repo.CreateAffiliateOrder(ctx, domain.CreateOrderInput{
		AffiliateWechatID: wechatID,
		ProductSlug:       product.Slug,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sold-out product should be rejected, got %v", err)
	}
}
