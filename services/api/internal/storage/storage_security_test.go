package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRejectsActiveSVG(t *testing.T) {
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if detected, err := Validate(data, 1<<20); err == nil {
		t.Fatalf("Validate accepted active SVG as %q", detected)
	} else if !errors.Is(err, ErrInvalidUpload) {
		t.Fatalf("Validate error %v is not ErrInvalidUpload", err)
	}
}

func TestGenerateStoredFilenameUsesDetectedMIMEExtension(t *testing.T) {
	name := GenerateStoredFilename("payload.html", "text/plain")
	if !strings.HasSuffix(name, ".txt") {
		t.Fatalf("stored filename %q retained attacker-controlled extension", name)
	}
}

func TestLocalUploadCannotBeServedAsHTML(t *testing.T) {
	adapter, err := NewLocalAdapter(t.TempDir(), "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	object, err := adapter.Put(context.Background(), PutInput{
		Data:         []byte("console.log('stored-xss')"),
		OriginalName: "payload.html",
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/uploads/"+object.StoredFilename, nil)
	response := httptest.NewRecorder()
	adapter.ServeFile(response, request, object.StoredFilename)

	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("Content-Type=%q, want text/plain", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q, want nosniff", got)
	}
	if got := response.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("Content-Disposition=%q, want attachment", got)
	}
}
