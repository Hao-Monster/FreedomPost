package paidaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAdminClientUsesPaidAccessRouteContract(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret")
	ctx := context.Background()
	if _, err := client.ListReaderAccounts(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResetReaderPassword(ctx, "account-1", "hash", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListArticleOrders(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateArticleOrderStatus(ctx, "order-1", "completed", "admin"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /internal/reader-accounts",
		"POST /internal/reader-accounts/account-1/reset-password",
		"GET /internal/article-orders",
		"PATCH /internal/article-orders/order-1",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths=%v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d]=%q, want %q", i, paths[i], want[i])
		}
	}
}

func TestClientRejectsEveryNon2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret")
	if _, err := client.ListArticleOrders(context.Background(), "admin"); err == nil {
		t.Fatal("404 response was treated as success")
	}
}
