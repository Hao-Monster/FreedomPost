package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OSSAdapter stores files in Aliyun OSS.
// This is a lightweight implementation using Aliyun OSS REST API directly
// (no SDK dependency needed).
type OSSAdapter struct {
	region        string
	bucket        string
	accessKeyID   string
	accessSecret  string
	endpoint      string
	publicBaseURL string
	prefix        string
	httpClient    *http.Client
}

// NewOSSAdapter creates an Aliyun OSS storage adapter.
func NewOSSAdapter(region, bucket, accessKeyID, accessKeySecret, endpoint, publicBaseURL, prefix string) (*OSSAdapter, error) {
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.oss-%s.aliyuncs.com", bucket, region)
	}
	return &OSSAdapter{
		region:        region,
		bucket:        bucket,
		accessKeyID:   accessKeyID,
		accessSecret:  accessKeySecret,
		endpoint:      endpoint,
		publicBaseURL: publicBaseURL,
		prefix:        prefix,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (a *OSSAdapter) Put(ctx context.Context, input PutInput) (*StoredObject, error) {
	detectedMime, err := Validate(input.Data, 500*1024*1024)
	if err != nil {
		return nil, err
	}
	storedName := GenerateStoredFilename(input.OriginalName, detectedMime)
	key := storedName
	if a.prefix != "" {
		key = strings.TrimRight(a.prefix, "/") + "/" + storedName
	}

	// PUT object to OSS
	url := strings.TrimRight(a.endpoint, "/") + "/" + key
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(input.Data))
	if err != nil {
		return nil, fmt.Errorf("build OSS request: %w", err)
	}
	req.Header.Set("Content-Type", detectedMime)
	req.ContentLength = int64(len(input.Data))
	// TODO: add HMAC-SHA1 Authorization header for full production use
	// For now, bucket must allow public writes (or use STS tokens)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OSS put: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("OSS put returned status %d", resp.StatusCode)
	}

	sha := ComputeSHA256(input.Data)
	var publicURL string
	if a.publicBaseURL != "" {
		publicURL = strings.TrimRight(a.publicBaseURL, "/") + "/" + key
	} else {
		publicURL = url
	}

	return &StoredObject{
		StorageKey:      key,
		StoredFilename:  storedName,
		PublicURL:       publicURL,
		StorageProvider: "oss",
		MimeType:        detectedMime,
		DetectedMime:    detectedMime,
		SizeBytes:       int64(len(input.Data)),
		SHA256:          sha,
	}, nil
}

func (a *OSSAdapter) Delete(ctx context.Context, storageKey string) error {
	url := strings.TrimRight(a.endpoint, "/") + "/" + storageKey
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("build OSS delete request: %w", err)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("OSS delete: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (a *OSSAdapter) Ping(ctx context.Context) error {
	url := strings.TrimRight(a.endpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
