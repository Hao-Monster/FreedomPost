// Package storage provides file storage adapters for Local disk, Aliyun OSS,
// and Cloudflare R2. All adapters implement the Adapter interface.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/uuid"
)

// ─── Interface ────────────────────────────────────────────────────────────────

// PutInput describes a file to store.
type PutInput struct {
	Data         []byte
	OriginalName string
	OwnerType    string // "post" | "comment"
	OwnerID      string // optional
	UploaderType string // "admin" | "public-comment"
}

// StoredObject is the result of a successful upload.
type StoredObject struct {
	StorageKey      string
	StoredFilename  string
	PublicURL       string
	StorageProvider string // "local" | "oss" | "r2"
	MimeType        string
	DetectedMime    string
	SizeBytes       int64
	SHA256          string
}

// Adapter is the common interface for all storage backends.
type Adapter interface {
	Put(ctx context.Context, input PutInput) (*StoredObject, error)
	Delete(ctx context.Context, storageKey string) error
	Ping(ctx context.Context) error
}

// ─── Validation ───────────────────────────────────────────────────────────────

// ErrInvalidUpload identifies client-controlled content validation failures.
// Callers can return a bounded 4xx response without exposing backend errors.
var ErrInvalidUpload = errors.New("invalid upload")

// AllowedMimeTypes is the server-authoritative whitelist of uploadable types.
var AllowedMimeTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true,
	"image/webp": true, "image/avif": true,
	"application/pdf": true,
	"text/plain":      true, "text/markdown": true,
	"audio/mpeg": true, "audio/ogg": true,
	"video/mp4": true, "video/webm": true,
	"application/zip":          true,
	"application/octet-stream": true,
}

// Validate checks magic bytes and file size. Returns the detected MIME type.
// This is the authoritative type check — client-supplied Content-Type is ignored.
func Validate(data []byte, maxBytes int64) (detectedMime string, err error) {
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("%w: file size %d exceeds limit %d", ErrInvalidUpload, len(data), maxBytes)
	}
	detected := mimetype.Detect(data)
	mt := detected.String()
	// Strip parameters (e.g. "text/plain; charset=utf-8" → "text/plain")
	base, _, _ := mime.ParseMediaType(mt)
	if base == "" {
		base = mt
	}
	if !AllowedMimeTypes[base] {
		return "", fmt.Errorf("%w: file type %q is not allowed", ErrInvalidUpload, base)
	}
	return base, nil
}

// ComputeSHA256 returns the hex SHA-256 of data.
func ComputeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var canonicalExtensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif",
	"image/webp": ".webp", "image/avif": ".avif", "application/pdf": ".pdf",
	"text/plain": ".txt", "text/markdown": ".md", "audio/mpeg": ".mp3",
	"audio/ogg": ".ogg", "video/mp4": ".mp4", "video/webm": ".webm",
	"application/zip": ".zip", "application/octet-stream": ".bin",
}

// GenerateStoredFilename builds "uuid.ext" from the detected MIME type. The
// attacker-controlled original extension must never influence HTTP serving.
func GenerateStoredFilename(_ string, mimeType string) string {
	id := uuid.NewString()
	ext := canonicalExtensions[mimeType]
	if ext == "" {
		ext = ".bin"
	}
	return id + ext
}

func storedMIMEType(storageKey string) string {
	ext := strings.ToLower(filepath.Ext(storageKey))
	for mimeType, canonicalExt := range canonicalExtensions {
		if ext == canonicalExt {
			return mimeType
		}
	}
	return "application/octet-stream"
}

func requiresDownload(mimeType string) bool {
	return !strings.HasPrefix(mimeType, "image/") &&
		!strings.HasPrefix(mimeType, "audio/") &&
		!strings.HasPrefix(mimeType, "video/")
}

// ─── Local storage ────────────────────────────────────────────────────────────

// LocalAdapter stores files on the local filesystem.
type LocalAdapter struct {
	root          string
	publicBaseURL string
}

// NewLocalAdapter creates a local filesystem storage adapter.
func NewLocalAdapter(root, publicBaseURL string) (*LocalAdapter, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local storage root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create local storage root: %w", err)
	}
	return &LocalAdapter{root: abs, publicBaseURL: publicBaseURL}, nil
}

func (a *LocalAdapter) Put(_ context.Context, input PutInput) (*StoredObject, error) {
	detectedMime, err := Validate(input.Data, 500*1024*1024)
	if err != nil {
		return nil, err
	}
	storedName := GenerateStoredFilename(input.OriginalName, detectedMime)
	dest := filepath.Join(a.root, storedName)
	if err := os.WriteFile(dest, input.Data, 0o640); err != nil {
		return nil, fmt.Errorf("write local file: %w", err)
	}
	sha := ComputeSHA256(input.Data)
	publicURL := strings.TrimRight(a.publicBaseURL, "/") + "/" + storedName
	return &StoredObject{
		StorageKey:      storedName,
		StoredFilename:  storedName,
		PublicURL:       publicURL,
		StorageProvider: "local",
		MimeType:        detectedMime,
		DetectedMime:    detectedMime,
		SizeBytes:       int64(len(input.Data)),
		SHA256:          sha,
	}, nil
}

func (a *LocalAdapter) Delete(_ context.Context, storageKey string) error {
	// Safety: ensure path stays within root
	clean := filepath.Clean(filepath.Join(a.root, filepath.Base(storageKey)))
	if !strings.HasPrefix(clean, a.root+string(filepath.Separator)) && clean != a.root {
		return fmt.Errorf("delete: path escapes storage root")
	}
	if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete local file: %w", err)
	}
	return nil
}

func (a *LocalAdapter) Ping(_ context.Context) error {
	_, err := os.Stat(a.root)
	return err
}

// ServeFile securely serves a local upload, preventing path traversal.
// Should be called from the uploads HTTP handler.
func (a *LocalAdapter) ServeFile(w http.ResponseWriter, r *http.Request, storageKey string) {
	// 1. Clean the path
	clean := filepath.Clean("/" + storageKey)
	// 2. Build and verify absolute target path
	target := filepath.Join(a.root, filepath.FromSlash(clean))
	absTarget, err := filepath.Abs(target)
	if err != nil || !strings.HasPrefix(absTarget, a.root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	// 3. Stat via Lstat (don't follow symlinks)
	info, err := os.Lstat(absTarget)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// 4. Set long-lived cache header for immutable uploads
	mimeType := storedMIMEType(absTarget)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if requiresDownload(mimeType) {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(absTarget)}))
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, absTarget)
}

// ─── HTTP ping helper ─────────────────────────────────────────────────────────

func httpPing(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// suppress unused warning
var _ = httpPing
