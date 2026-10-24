package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ManagedAdapter is configured with a separate private root/bucket. Open is
// called only after application authorization; it never redirects to an origin.
type ManagedAdapter interface {
	Adapter
	Provider() string
	KeyForFilename(string) (string, error)
	Open(ctx context.Context, key, method, rangeHeader string) (*ObjectRead, error)
}

type ObjectRead struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ContentRange  string
}

func validStoredFilename(name string) bool {
	ext := filepath.Ext(name)
	id := strings.TrimSuffix(name, ext)
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || name != filepath.Base(name) {
		return false
	}
	for _, allowed := range canonicalExtensions {
		if ext == allowed {
			return true
		}
	}
	return false
}

func resolveStoredFilename(input PutInput, detectedMime string) (string, error) {
	if input.StoredFilename == "" {
		return GenerateStoredFilename(input.OriginalName, detectedMime), nil
	}
	if !validStoredFilename(input.StoredFilename) || filepath.Ext(input.StoredFilename) != canonicalExtensions[detectedMime] {
		return "", fmt.Errorf("%w: invalid stored filename", ErrInvalidUpload)
	}
	return input.StoredFilename, nil
}

func keyForFilename(prefix, name string) (string, error) {
	if !validStoredFilename(name) {
		return "", fmt.Errorf("%w: invalid stored filename", ErrInvalidUpload)
	}
	if prefix == "" {
		return name, nil
	}
	return strings.TrimRight(prefix, "/") + "/" + name, nil
}

func (a *LocalAdapter) Provider() string                           { return "local" }
func (a *R2Adapter) Provider() string                              { return "r2" }
func (a *OSSAdapter) Provider() string                             { return "oss" }
func (a *LocalAdapter) KeyForFilename(name string) (string, error) { return keyForFilename("", name) }
func (a *R2Adapter) KeyForFilename(name string) (string, error) {
	return keyForFilename(a.prefix, name)
}
func (a *OSSAdapter) KeyForFilename(name string) (string, error) {
	return keyForFilename(a.prefix, name)
}

type sectionReadCloser struct {
	io.Reader
	io.Closer
}

func (a *LocalAdapter) Open(_ context.Context, key, method, rangeHeader string) (*ObjectRead, error) {
	if method != http.MethodGet && method != http.MethodHead {
		return nil, fmt.Errorf("invalid read method")
	}
	// New keys are UUID filenames; historical local uploads may have nested
	// prefixes. Both are read without aliases, traversal or symlink following.
	path, info, err := a.objectPath(key)
	if err != nil {
		return nil, err
	}
	start, length, status := int64(0), info.Size(), http.StatusOK
	contentRange := ""
	if method == http.MethodGet && rangeHeader != "" {
		start, length, err = singleRange(rangeHeader, info.Size())
		if err != nil {
			return &ObjectRead{StatusCode: http.StatusRequestedRangeNotSatisfiable, ContentRange: fmt.Sprintf("bytes */%d", info.Size())}, nil
		}
		status = http.StatusPartialContent
		contentRange = fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, info.Size())
	}
	result := &ObjectRead{StatusCode: status, ContentLength: length, ContentRange: contentRange}
	if method == http.MethodHead {
		return result, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	result.Body = &sectionReadCloser{Reader: io.NewSectionReader(file, start, length), Closer: file}
	return result, nil
}

func (a *LocalAdapter) objectPath(key string) (string, os.FileInfo, error) {
	invalid := fmt.Errorf("invalid local object key")
	if key == "" || strings.ContainsAny(key, "\\:\x00") || strings.HasPrefix(key, "/") {
		return "", nil, invalid
	}
	parts := strings.Split(key, "/")
	current := a.root
	var info os.FileInfo
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, invalid
		}
		current = filepath.Join(current, part)
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, invalid
		}
		if i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", nil, invalid
		}
	}
	return current, info, nil
}

// A single range supports browser audio/video and downloads without buffering.
// Multipart ranges are rejected rather than allowing unbounded amplification.
func singleRange(value string, size int64) (int64, int64, error) {
	invalid := fmt.Errorf("invalid byte range")
	if size <= 0 || !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, invalid
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, invalid
	}
	if parts[0] == "" {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, invalid
		}
		if n > size {
			n = size
		}
		return size - n, n, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, invalid
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, invalid
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, nil
}

func managedRequest(ctx context.Context, address, method, rangeHeader string) (*http.Request, error) {
	if method != http.MethodGet && method != http.MethodHead {
		return nil, fmt.Errorf("invalid read method")
	}
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, err
	}
	if method == http.MethodGet && rangeHeader != "" {
		if len(rangeHeader) > 100 || !strings.HasPrefix(rangeHeader, "bytes=") || strings.Contains(rangeHeader, ",") {
			return nil, fmt.Errorf("invalid byte range")
		}
		req.Header.Set("Range", rangeHeader)
	}
	return req, nil
}

func readObject(client *http.Client, req *http.Request) (*ObjectRead, error) {
	// Credentials stay at the configured object service even if it responds 3xx.
	reader := *client
	reader.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := reader.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		return nil, fmt.Errorf("object read returned status %d", resp.StatusCode)
	}
	return &ObjectRead{Body: resp.Body, StatusCode: resp.StatusCode, ContentLength: resp.ContentLength, ContentRange: resp.Header.Get("Content-Range")}, nil
}

func (a *R2Adapter) Open(ctx context.Context, key, method, rangeHeader string) (*ObjectRead, error) {
	req, err := managedRequest(ctx, strings.TrimRight(a.endpoint, "/")+"/"+a.bucket+"/"+key, method, rangeHeader)
	if err != nil {
		return nil, err
	}
	if err := a.sign(req, nil, time.Now().UTC(), req.URL.EscapedPath()); err != nil {
		return nil, err
	}
	return readObject(a.httpClient, req)
}

func (a *OSSAdapter) Open(ctx context.Context, key, method, rangeHeader string) (*ObjectRead, error) {
	req, err := managedRequest(ctx, strings.TrimRight(a.endpoint, "/")+"/"+key, method, rangeHeader)
	if err != nil {
		return nil, err
	}
	a.sign(req, key)
	return readObject(a.httpClient, req)
}
