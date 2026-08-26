package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOSSRequestsAreAuthenticatedAndSafeToDownload(t *testing.T) {
	var observed *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observed = request.Clone(context.Background())
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter, err := NewOSSAdapter("cn-test", "bucket", "access-id", "access-secret", server.URL, server.URL, "uploads")
	if err != nil {
		t.Fatal(err)
	}
	object, err := adapter.Put(context.Background(), PutInput{Data: []byte("plain attachment"), OriginalName: "payload.html"})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil || !strings.HasPrefix(observed.Header.Get("Authorization"), "OSS access-id:") {
		t.Fatalf("missing OSS Authorization header: %#v", observed)
	}
	if observed.Header.Get("Content-MD5") == "" || observed.Header.Get("Date") == "" {
		t.Fatalf("missing signed integrity headers: %#v", observed.Header)
	}
	if !strings.HasPrefix(observed.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unsafe content disposition: %q", observed.Header.Get("Content-Disposition"))
	}
	if !strings.HasSuffix(object.StoredFilename, ".txt") {
		t.Fatalf("stored filename=%q", object.StoredFilename)
	}
}
