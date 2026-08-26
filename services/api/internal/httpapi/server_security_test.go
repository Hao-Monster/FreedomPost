package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsTrailingValue(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"value":"first"}{"value":"second"}`))
	response := httptest.NewRecorder()
	var input struct {
		Value string `json:"value"`
	}
	if decodeJSON(response, request, &input) {
		t.Fatal("multiple JSON values should be rejected")
	}
	if response.Code != 400 || !strings.Contains(response.Body.String(), `"code":"INVALID_JSON"`) {
		t.Fatalf("unexpected response: status=%d body=%s", response.Code, response.Body.String())
	}
}
