package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedLocalReservationsAndRanges(t *testing.T) {
	adapter, err := NewLocalAdapter(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	name := GenerateStoredFilename("report.html", "text/plain")
	key, err := adapter.KeyForFilename(name)
	if err != nil {
		t.Fatal(err)
	}
	object, err := adapter.Put(context.Background(), PutInput{Data: []byte("0123456789"), StoredFilename: name})
	if err != nil || object.StorageKey != key {
		t.Fatalf("reserved key not honored: %v", err)
	}
	for _, c := range []struct {
		method, value      string
		status             int
		body, contentRange string
	}{
		{"GET", "", 200, "0123456789", ""}, {"HEAD", "", 200, "", ""},
		{"GET", "bytes=2-4", 206, "234", "bytes 2-4/10"}, {"GET", "bytes=-3", 206, "789", "bytes 7-9/10"},
		{"GET", "bytes=99-", 416, "", "bytes */10"}, {"GET", "bytes=0-1,3-4", 416, "", "bytes */10"},
	} {
		t.Run(c.method+c.value, func(t *testing.T) {
			read, err := adapter.Open(context.Background(), key, c.method, c.value)
			if err != nil {
				t.Fatal(err)
			}
			var body []byte
			if read.Body != nil {
				defer read.Body.Close()
				body, err = io.ReadAll(read.Body)
			}
			if err != nil || read.StatusCode != c.status || string(body) != c.body || read.ContentRange != c.contentRange {
				t.Fatalf("unexpected range response: %+v body=%q err=%v", read, body, err)
			}
		})
	}
	for _, bad := range []string{"../" + name, name + ".html", strings.Replace(name, ".txt", ".png", 1), "x.txt"} {
		if _, err := adapter.Put(context.Background(), PutInput{Data: []byte("text"), StoredFilename: bad}); err == nil {
			t.Fatalf("accepted invalid reservation %q", bad)
		}
	}
}

func TestManagedLocalCanMigrateNestedHistoricalKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "old", "2025"), 0700); err != nil {
		t.Fatal(err)
	}
	key := "old/2025/legacy-name.txt"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(key)), []byte("old bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, _ := NewLocalAdapter(root, "")
	object, err := adapter.Open(context.Background(), key, "GET", "")
	if err != nil {
		t.Fatal(err)
	}
	object.Body.Close()
	for _, bad := range []string{"../legacy-name.txt", "/old/2025/legacy-name.txt", "old/../old/2025/legacy-name.txt", "old//2025/legacy-name.txt", "old\\2025\\legacy-name.txt"} {
		if _, err := adapter.Open(context.Background(), bad, "GET", ""); err == nil {
			t.Errorf("accepted aliased storage key %s", bad)
		}
	}
	if err := adapter.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); !os.IsNotExist(err) {
		t.Fatal("nested original was not retired")
	}
}

func TestManagedRemoteReadsAreSignedAndNeverRedirect(t *testing.T) {
	for _, provider := range []string{"r2", "oss"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") == "" {
					t.Error("unsigned object read")
				}
				if r.URL.Path == "/redirect" || strings.HasSuffix(r.URL.Path, "/redirect") {
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(302)
					return
				}
				if r.Method == "HEAD" {
					w.Header().Set("Content-Length", "10")
					return
				}
				if r.Header.Get("Range") != "bytes=2-4" {
					t.Errorf("lost range %q", r.Header.Get("Range"))
				}
				w.Header().Set("Content-Range", "bytes 2-4/10")
				w.WriteHeader(206)
				_, _ = w.Write([]byte("234"))
			}))
			defer origin.Close()
			var adapter ManagedAdapter
			if provider == "r2" {
				adapter, _ = NewR2Adapter("qa", "private", "qa-key", "qa-secret", origin.URL, "", "")
			} else {
				adapter, _ = NewOSSAdapter("qa", "private", "qa-key", "qa-secret", origin.URL, "", "")
			}
			result, err := adapter.Open(context.Background(), "object.txt", "GET", "bytes=2-4")
			if err != nil {
				t.Fatal(err)
			}
			result.Body.Close()
			if result.StatusCode != 206 || result.ContentRange != "bytes 2-4/10" {
				t.Fatal("range metadata lost")
			}
			result, err = adapter.Open(context.Background(), "object.txt", "HEAD", "")
			if err != nil {
				t.Fatal(err)
			}
			result.Body.Close()
			if result.ContentLength != 10 {
				t.Fatal("HEAD metadata lost")
			}
			if _, err = adapter.Open(context.Background(), "redirect", "GET", ""); err == nil {
				t.Fatal("accepted redirect")
			}
			if requests != 3 {
				t.Fatal("followed origin redirect")
			}
		})
	}
}
