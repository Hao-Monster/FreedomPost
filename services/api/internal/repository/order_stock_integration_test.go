package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

func assertOrderInventory(t *testing.T, repo *Postgres, id string, stock, sold int, reserved bool, status string) {
	t.Helper()
	var gotStock, gotSold int
	var gotReserved bool
	var gotStatus string
	err := repo.pool.QueryRow(context.Background(), `SELECT p.stock,p.sold_count,o.stock_reserved,o.order_status FROM affiliate_orders o JOIN products p ON p.id=o.product_id WHERE o.id=$1`, id).Scan(&gotStock, &gotSold, &gotReserved, &gotStatus)
	if err != nil || gotStock != stock || gotSold != sold || gotReserved != reserved || gotStatus != status {
		t.Fatalf("inventory got stock=%d sold=%d reserved=%v status=%s; want %d/%d/%v/%s: %v", gotStock, gotSold, gotReserved, gotStatus, stock, sold, reserved, status, err)
	}
}

func TestOrderStockContactIntentAndConfirmationTransitions(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	if _, err := repo.pool.Exec(ctx, `UPDATE products SET stock=1 WHERE slug=$1`, input.ProductSlug); err != nil {
		t.Fatal(err)
	}
	order, err := repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
	if err != nil {
		t.Fatal(err)
	}
	assertOrderInventory(t, repo, order.ID, 1, 0, false, "pending")
	for _, step := range []struct {
		status      string
		stock, sold int
		reserved    bool
	}{
		{"pending", 1, 0, false}, {"canceled", 1, 0, false}, {"pending", 1, 0, false},
		{"completed", 0, 1, true}, {"completed", 0, 1, true},
		// Previously allocated units remain allocated when staff reopen contact.
		{"pending", 0, 0, true}, {"completed", 0, 1, true},
		{"canceled", 1, 0, false}, {"canceled", 1, 0, false}, {"pending", 1, 0, false}, {"completed", 0, 1, true},
	} {
		if _, err = repo.UpdateAffiliateOrderStatus(ctx, order.ID, step.status, "not_due", ""); err != nil {
			t.Fatalf("%s: %v", step.status, err)
		}
		assertOrderInventory(t, repo, order.ID, step.stock, step.sold, step.reserved, step.status)
	}
}

func TestOrderStockHistoricalReservationsAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		name, status        string
		stock, sold         int
		reserved            bool
		target              string
		wantStock, wantSold int
		wantReserved        bool
	}{
		{"legacy reserved pending completes", "pending", 0, 0, true, "completed", 0, 1, true},
		{"legacy reserved pending cancels", "pending", 0, 0, true, "canceled", 1, 0, false},
		{"legacy reserved pending save", "pending", 0, 0, true, "pending", 0, 0, true},
		{"legacy unreserved pending completes", "pending", 1, 0, false, "completed", 0, 1, true},
		{"legacy completed save never backfills allocation", "completed", 0, 1, false, "completed", 0, 1, false},
		{"legacy completed cancellation never invents stock", "completed", 0, 1, false, "canceled", 0, 0, false},
		{"unlimited completion", "pending", -1, 0, false, "completed", -1, 1, false},
		{"unlimited cancellation", "completed", -1, 1, false, "canceled", -1, 0, false},
		{"changed to unlimited before cancellation", "pending", -1, 0, true, "canceled", -1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, input := orderHandoffFixture(t)
			ctx := context.Background()
			order, err := repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(ctx, `UPDATE affiliate_orders SET order_status=$2,stock_reserved=$3 WHERE id=$1`, order.ID, tc.status, tc.reserved); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(ctx, `UPDATE products SET stock=$2,sold_count=$3 WHERE id=$1`, order.ProductID, tc.stock, tc.sold); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.UpdateAffiliateOrderStatus(ctx, order.ID, tc.target, "not_due", ""); err != nil {
				t.Fatal(err)
			}
			assertOrderInventory(t, repo, order.ID, tc.wantStock, tc.wantSold, tc.wantReserved, tc.target)
		})
	}
}

func TestOrderStockOnlyOneConcurrentSaleCanConsumeLastUnit(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := repo.pool.Exec(ctx, `UPDATE products SET stock=1 WHERE slug=$1`, input.ProductSlug); err != nil {
		t.Fatal(err)
	}
	orders := make([]*domain.AffiliateOrder, 2)
	for i := range orders {
		var err error
		orders[i], err = repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range orders {
		assertOrderInventory(t, repo, o.ID, 1, 0, false, "pending")
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, o := range orders {
		go func(id string) {
			<-start
			_, err := repo.UpdateAffiliateOrderStatus(ctx, id, "completed", "pending", "")
			results <- err
		}(o.ID)
	}
	close(start)
	winners, conflicts := 0, 0
	for range orders {
		err := <-results
		if err == nil {
			winners++
		} else if errors.Is(err, domain.ErrInvalidState) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("completed=%d conflicts=%d", winners, conflicts)
	}
	var stock, sold, allocated, completed int
	err := repo.pool.QueryRow(ctx, `SELECT p.stock,p.sold_count,(SELECT count(*) FROM affiliate_orders WHERE product_id=p.id AND stock_reserved),(SELECT count(*) FROM affiliate_orders WHERE product_id=p.id AND order_status='completed') FROM products p WHERE slug=$1`, input.ProductSlug).Scan(&stock, &sold, &allocated, &completed)
	if err != nil || stock != 0 || sold != 1 || allocated != 1 || completed != 1 {
		t.Fatalf("oversold stock=%d sold=%d allocated=%d completed=%d: %v", stock, sold, allocated, completed, err)
	}
}

func TestOrderStockConcurrentRepeatedConfirmationAndCancellation(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := repo.pool.Exec(ctx, `UPDATE products SET stock=1 WHERE slug=$1`, input.ProductSlug); err != nil {
		t.Fatal(err)
	}
	order, err := repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"completed", "canceled"} {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := repo.UpdateAffiliateOrderStatus(ctx, order.ID, status, "not_due", "")
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if status == "completed" {
			assertOrderInventory(t, repo, order.ID, 0, 1, true, status)
		} else {
			assertOrderInventory(t, repo, order.ID, 1, 0, false, status)
		}
	}
}

func TestOrderStockUnavailableAndDeletedProduct(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	order, err := repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE products SET stock=0 WHERE id=$1`, order.ProductID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateAffiliateOrderStatus(ctx, order.ID, "completed", "pending", "must rollback"); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("stock conflict: %v", err)
	}
	assertOrderInventory(t, repo, order.ID, 0, 0, false, "pending")
	// Retain existing snapshot behavior when staff deliberately removed the product.
	if _, err = repo.pool.Exec(ctx, `DELETE FROM products WHERE id=$1`, order.ProductID); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.UpdateAffiliateOrderStatus(ctx, order.ID, "completed", "pending", "")
	if err != nil || updated == nil || updated.ProductID != "" || updated.ProductTitle != order.ProductTitle {
		t.Fatalf("deleted product snapshot: %+v %v", updated, err)
	}
	var reserved bool
	if err = repo.pool.QueryRow(ctx, `SELECT stock_reserved FROM affiliate_orders WHERE id=$1`, order.ID).Scan(&reserved); err != nil || reserved {
		t.Fatalf("deleted product created allocation: %v %v", reserved, err)
	}
}

func TestOrderStockConcurrentConfirmationAndCancellationRemainConsistent(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := repo.pool.Exec(ctx, `UPDATE products SET stock=1 WHERE slug=$1`, input.ProductSlug); err != nil {
		t.Fatal(err)
	}
	order, err := repo.CreateAffiliateOrder(ctx, input.CreateOrderInput)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 12)
	for i := range 12 {
		status := "completed"
		if i%2 == 0 {
			status = "canceled"
		}
		go func() {
			<-start
			_, err := repo.UpdateAffiliateOrderStatus(ctx, order.ID, status, "not_due", "")
			errs <- err
		}()
	}
	close(start)
	for range 12 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := repo.pool.QueryRow(ctx, `SELECT order_status FROM affiliate_orders WHERE id=$1`, order.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "completed" {
		assertOrderInventory(t, repo, order.ID, 0, 1, true, status)
	} else if status == "canceled" {
		assertOrderInventory(t, repo, order.ID, 1, 0, false, status)
	} else {
		t.Fatalf("unexpected final status %s", status)
	}
}

func TestOwnedOrderLookupChecksOwnerAndInputBeforeCreation(t *testing.T) {
	repo, input := orderHandoffFixture(t)
	ctx := context.Background()
	if _, err := repo.LookupOwnedAffiliateOrder(ctx, input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing lookup: %v", err)
	}
	h, _, err := repo.CreateOwnedAffiliateOrder(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.LookupOwnedAffiliateOrder(ctx, input)
	if err != nil || replay.Order.ID != h.Order.ID {
		t.Fatalf("lookup: %+v %v", replay, err)
	}
	changed := input
	changed.ProductSlug = "different"
	if _, err = repo.LookupOwnedAffiliateOrder(ctx, changed); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("input conflict: %v", err)
	}
	changed = input
	changed.OwnerHash = "other-owner"
	if _, err = repo.LookupOwnedAffiliateOrder(ctx, changed); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("owner scope: %v", err)
	}
}
