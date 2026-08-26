package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

func decodeProductInputForTest(t *testing.T, body string) domain.ProductInput {
	t.Helper()
	var input domain.ProductInput
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatalf("decode product: %v", err)
	}
	return input
}

func TestNormalizeProductInputRequiresCompleteSafeContract(t *testing.T) {
	valid := decodeProductInputForTest(t, `{
		"title":"安全商品","summary":"摘要","description":"详情",
		"priceCents":9900,"compareAtCents":12900,"stock":-1,
		"status":"published","sortOrder":0,"coverUrl":"/api/uploads/cover.png"
	}`)
	if normalized, ok := normalizeProductInput(valid); !ok || normalized.Stock != -1 || normalized.Currency != "CNY" {
		t.Fatalf("valid complete product rejected: ok=%v input=%+v", ok, normalized)
	}

	missingStock := decodeProductInputForTest(t, `{
		"title":"安全商品","summary":"摘要","description":"详情",
		"priceCents":9900,"compareAtCents":null,
		"status":"published","sortOrder":0
	}`)
	if _, ok := normalizeProductInput(missingStock); ok {
		t.Fatal("omitted stock should not silently become zero")
	}

	unsafeURL := decodeProductInputForTest(t, `{
		"title":"安全商品","summary":"摘要","description":"详情",
		"priceCents":9900,"compareAtCents":null,"stock":-1,
		"status":"published","sortOrder":0,"coverUrl":"javascript:alert(1)"
	}`)
	if _, ok := normalizeProductInput(unsafeURL); ok {
		t.Fatal("active-content product URL should be rejected")
	}
}
