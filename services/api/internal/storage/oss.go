package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"mime"
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
	if bucket == "" || accessKeyID == "" || accessKeySecret == "" {
		return nil, fmt.Errorf("OSS bucket and access credentials are required")
	}
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
	contentMD5 := md5.Sum(input.Data)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(contentMD5[:]))
	if requiresDownload(detectedMime) {
		req.Header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": storedName}))
	}
	a.sign(req, key)

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
	a.sign(req, storageKey)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("OSS delete: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("OSS delete returned status %d", resp.StatusCode)
	}
	return nil
}

func (a *OSSAdapter) Ping(ctx context.Context) error {
	url := strings.TrimRight(a.endpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	a.sign(req, "")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("OSS ping returned status %d", resp.StatusCode)
	}
	return nil
}

// sign adds an OSS V1 Authorization header. OSS still supports V1 for REST
// requests; unlike the previous implementation this never requires a bucket
// with anonymous write permission.
func (a *OSSAdapter) sign(req *http.Request, key string) {
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	canonicalResource := "/" + a.bucket + "/" + strings.TrimLeft(key, "/")
	stringToSign := strings.Join([]string{
		req.Method,
		req.Header.Get("Content-MD5"),
		req.Header.Get("Content-Type"),
		date,
		canonicalResource,
	}, "\n")
	mac := hmac.New(sha1.New, []byte(a.accessSecret))
	_, _ = mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", "OSS "+a.accessKeyID+":"+signature)
}
