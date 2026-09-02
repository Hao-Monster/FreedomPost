package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestProductValidationExplainsCompareAtBelowPrice(t *testing.T) {
	input := decodeProductInputForTest(t, `{
		"title":"远程安装服务","summary":"协助完成远程安装","description":"预约后联系管理员安排服务。",
		"category":"service","priceCents":3000,"commissionCents":500,"compareAtCents":500,
		"currency":"CNY","stock":-1,"soldCount":19,"status":"published","sortOrder":2
	}`)

	_, issues := normalizeProductInputWithIssues(input)
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %#v", issues)
	}
	issue := issues[0]
	if issue.Field != "compareAtCents" || issue.Code != "COMPARE_AT_BELOW_PRICE" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if !strings.Contains(issue.Resolution, "30.00 CNY") {
		t.Fatalf("resolution should include the required minimum: %#v", issue)
	}
}

func TestProductValidationReturnsAllFieldIssues(t *testing.T) {
	input := decodeProductInputForTest(t, `{
		"title":" ","summary":"","description":"","category":"service",
		"priceCents":-1,"commissionCents":100000001,"compareAtCents":null,"currency":"CNY",
		"stock":-2,"soldCount":1000001,"status":"invalid","sortOrder":100001,
		"coverUrl":"javascript:alert(1)"
	}`)

	_, issues := normalizeProductInputWithIssues(input)
	fields := make(map[string]bool, len(issues))
	for _, issue := range issues {
		fields[issue.Field] = true
	}
	for _, field := range []string{"title", "summary", "description", "priceCents", "commissionCents", "stock", "soldCount", "status", "sortOrder", "coverUrl"} {
		if !fields[field] {
			t.Errorf("missing issue for %s: %#v", field, issues)
		}
	}
}

func TestWriteProductValidationErrorPreservesCompatibleEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProductValidationError(recorder, []productInputIssue{{
		Field: "title", Code: "REQUIRED", Message: "商品名称不能为空", Resolution: "请输入商品名称",
	}})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"code":"INVALID_PRODUCT"`, `"issues"`, `"field":"title"`, `"resolution":"请输入商品名称"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response %s does not contain %s", body, expected)
		}
	}
}

func TestRequestIDMiddlewareAddsResponseHeaderAndLogCorrelation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := requestIDMiddleware(requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	requestID := recorder.Header().Get("X-Request-ID")
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("invalid request id %q: %v", requestID, err)
	}
	if !strings.Contains(logs.String(), requestID) {
		t.Fatalf("request log does not contain request id %q: %s", requestID, logs.String())
	}
}

func TestRequestIDMiddlewareGeneratesUniqueIDs(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	first := httptest.NewRecorder()
	second := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/first", nil))
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/second", nil))
	if first.Header().Get(requestIDHeader) == second.Header().Get(requestIDHeader) {
		t.Fatalf("request IDs must be unique: %q", first.Header().Get(requestIDHeader))
	}
}

func TestCORSExposesRequestIDToAllowedAdminOrigin(t *testing.T) {
	handler := corsMiddleware(map[string]bool{"https://admin.example": true})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/admin/products", nil)
	request.Header.Set("Origin", "https://admin.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Expose-Headers") != requestIDHeader {
		t.Fatalf("request id header is not exposed: %#v", recorder.Header())
	}
}

func TestInternalErrorKeepsDetailsServerSideAndReturnsRequestID(t *testing.T) {
	const internalDetail = "postgres internal-detail constraint=products_slug_key"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	server := &Server{logger: logger}
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.internalError(w, r, errors.New(internalDetail))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/admin/products/id", nil))

	requestID := recorder.Header().Get("X-Request-ID")
	if requestID == "" || !strings.Contains(logs.String(), requestID) {
		t.Fatalf("request id is not correlated with error log: header=%q logs=%s", requestID, logs.String())
	}
	if strings.Contains(recorder.Body.String(), internalDetail) {
		t.Fatalf("internal detail leaked to client: %s", recorder.Body.String())
	}
}
