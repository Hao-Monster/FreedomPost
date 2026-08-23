package domain_test

import (
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

// TestBuildAffiliateProductView verifies that Go's calculation exactly matches
// TypeScript's buildAffiliateProductView() in apps/api/src/repositories/affiliate-pricing.ts
// (commit 2f1e598: "fix(api): preserve configured affiliate commission").
//
// Golden rule: commissionCents = baseCommissionCents + markupCommissionCents
// where baseCommissionCents comes from product.commissionCents (NOT recomputed).
func TestBuildAffiliateProductView(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		priceCents        int
		commissionCents   int
		markupPercent     int
		wantCustomerPrice int
		wantBase          int
		wantMarkup        int
		wantTotal         int
	}{
		{
			name:              "zero markup: no price change, base commission preserved",
			priceCents:        1000,
			commissionCents:   100,
			markupPercent:     0,
			wantCustomerPrice: 1000,
			wantBase:          100,
			wantMarkup:        0,
			wantTotal:         100,
		},
		{
			name:              "10% markup: customer pays 1100, markup earns 100",
			priceCents:        1000,
			commissionCents:   100,
			markupPercent:     10,
			wantCustomerPrice: 1100,
			wantBase:          100,
			wantMarkup:        100,
			wantTotal:         200,
		},
		{
			name:              "50% markup",
			priceCents:        1000,
			commissionCents:   100,
			markupPercent:     50,
			wantCustomerPrice: 1500,
			wantBase:          100,
			wantMarkup:        500,
			wantTotal:         600,
		},
		{
			name:              "rounding: Math.round(333 * 1.1) = 366",
			priceCents:        333,
			commissionCents:   30,
			markupPercent:     10,
			wantCustomerPrice: 366,
			wantBase:          30,
			wantMarkup:        33, // 366 - 333 = 33
			wantTotal:         63,
		},
		{
			name:              "rounding half-up: Math.round(999 * 1.1) = 1099",
			priceCents:        999,
			commissionCents:   50,
			markupPercent:     10,
			wantCustomerPrice: 1099,
			wantBase:          50,
			wantMarkup:        100, // 1099 - 999 = 100
			wantTotal:         150,
		},
		{
			name:              "large price: 29900 * 1.15 = 34385",
			priceCents:        29900,
			commissionCents:   1000,
			markupPercent:     15,
			wantCustomerPrice: 34385,
			wantBase:          1000,
			wantMarkup:        4485,
			wantTotal:         5485,
		},
		{
			name:              "zero price product",
			priceCents:        0,
			commissionCents:   0,
			markupPercent:     20,
			wantCustomerPrice: 0,
			wantBase:          0,
			wantMarkup:        0,
			wantTotal:         0,
		},
		{
			name:              "commission higher than price (unusual but valid)",
			priceCents:        100,
			commissionCents:   200,
			markupPercent:     0,
			wantCustomerPrice: 100,
			wantBase:          200,
			wantMarkup:        0,
			wantTotal:         200,
		},
		{
			name:              "100% markup doubles the price",
			priceCents:        500,
			commissionCents:   50,
			markupPercent:     100,
			wantCustomerPrice: 1000,
			wantBase:          50,
			wantMarkup:        500,
			wantTotal:         550,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			product := domain.Product{
				ID:              "test-product",
				PriceCents:      tc.priceCents,
				CommissionCents: tc.commissionCents,
			}
			view := domain.BuildAffiliateProductView(product, tc.markupPercent)

			if view.CustomerPriceCents != tc.wantCustomerPrice {
				t.Errorf("CustomerPriceCents = %d, want %d", view.CustomerPriceCents, tc.wantCustomerPrice)
			}
			if view.BaseCommissionCents != tc.wantBase {
				t.Errorf("BaseCommissionCents = %d, want %d", view.BaseCommissionCents, tc.wantBase)
			}
			if view.MarkupCommissionCents != tc.wantMarkup {
				t.Errorf("MarkupCommissionCents = %d, want %d", view.MarkupCommissionCents, tc.wantMarkup)
			}
			if view.CommissionCents != tc.wantTotal {
				t.Errorf("CommissionCents = %d, want %d", view.CommissionCents, tc.wantTotal)
			}
			// Invariant: CommissionCents = Base + Markup
			if view.CommissionCents != view.BaseCommissionCents+view.MarkupCommissionCents {
				t.Errorf("CommissionCents invariant violated: %d != %d + %d",
					view.CommissionCents, view.BaseCommissionCents, view.MarkupCommissionCents)
			}
			// Invariant: MarkupCommissionCents = CustomerPrice - ProductPrice
			if view.MarkupCommissionCents != view.CustomerPriceCents-view.Product.PriceCents {
				t.Errorf("MarkupCommissionCents invariant violated: %d != %d - %d",
					view.MarkupCommissionCents, view.CustomerPriceCents, view.Product.PriceCents)
			}
			// Invariant: MarkupPercent stored correctly
			if view.MarkupPercent != tc.markupPercent {
				t.Errorf("MarkupPercent = %d, want %d", view.MarkupPercent, tc.markupPercent)
			}
		})
	}
}
