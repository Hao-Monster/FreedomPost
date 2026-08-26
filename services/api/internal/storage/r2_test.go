package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestR2ForcesDownloadsAndReportsRemoteFailures(t *testing.T) {
	var putDisposition string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPut:
			putDisposition = request.Header.Get("Content-Disposition")
			response.WriteHeader(http.StatusOK)
		default:
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	adapter, err := NewR2Adapter("account", "bucket", "access", "secret", server.URL, "https://cdn.example.test", "uploads")
	if err != nil {
		t.Fatalf("NewR2Adapter: %v", err)
	}
	object, err := adapter.Put(context.Background(), PutInput{Data: []byte("plain text"), OriginalName: "payload.html"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasSuffix(object.StoredFilename, ".txt") || !strings.HasPrefix(putDisposition, "attachment;") {
		t.Fatalf("unsafe R2 metadata: filename=%q disposition=%q", object.StoredFilename, putDisposition)
	}
	if err := adapter.Delete(context.Background(), object.StorageKey); err == nil {
		t.Fatal("R2 delete should report a remote 500 response")
	}
	if err := adapter.Ping(context.Background()); err == nil {
		t.Fatal("R2 ping should report a remote 500 response")
	}
}
