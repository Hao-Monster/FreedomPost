package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestProductInputAcceptsCamelCaseAndLegacyImageURL(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewBufferString(`{
		"title":"安全商品",
		"imageUrl":"https://example.test/cover.png",
		"priceCents":9900,
		"commissionCents":900,
		"compareAtCents":12900,
		"soldCount":7,
		"sortOrder":3
	}`))
	decoder.DisallowUnknownFields()
	var input ProductInput
	if err := decoder.Decode(&input); err != nil {
		t.Fatalf("decode ProductInput: %v", err)
	}
	if input.PriceCents != 9900 || input.CommissionCents != 900 || input.CompareAtCents == nil || *input.CompareAtCents != 12900 {
		t.Fatalf("numeric camelCase fields were not decoded: %+v", input)
	}
	if input.CoverURL != "https://example.test/cover.png" {
		t.Fatalf("legacy imageUrl was not mapped to CoverURL: %+v", input)
	}
}

func TestProductInputRejectsTrailingJSONValue(t *testing.T) {
	var input ProductInput
	if err := json.Unmarshal([]byte(`{"title":"first"}{"title":"second"}`), &input); err == nil {
		t.Fatal("expected multiple JSON values to be rejected")
	}
}
