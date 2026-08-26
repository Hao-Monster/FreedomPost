package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// R2Adapter stores files in Cloudflare R2 using S3-compatible REST API.
// Uses AWS Signature Version 4 — no SDK dependency required.
type R2Adapter struct {
	accountID     string
	bucket        string
	accessKeyID   string
	secretKey     string
	endpoint      string
	publicBaseURL string
	prefix        string
	region        string
	httpClient    *http.Client
}

// NewR2Adapter creates a Cloudflare R2 storage adapter.
func NewR2Adapter(accountID, bucket, accessKeyID, secretKey, endpoint, publicBaseURL, prefix string) (*R2Adapter, error) {
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	}
	return &R2Adapter{
		accountID:     accountID,
		bucket:        bucket,
		accessKeyID:   accessKeyID,
		secretKey:     secretKey,
		endpoint:      endpoint,
		publicBaseURL: publicBaseURL,
		prefix:        prefix,
		region:        "auto",
		httpClient:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (a *R2Adapter) Put(ctx context.Context, input PutInput) (*StoredObject, error) {
	detectedMime, err := Validate(input.Data, 500*1024*1024)
	if err != nil {
		return nil, err
	}
	storedName := GenerateStoredFilename(input.OriginalName, detectedMime)
	key := storedName
	if a.prefix != "" {
		key = strings.TrimRight(a.prefix, "/") + "/" + storedName
	}

	path := "/" + a.bucket + "/" + key
	url := strings.TrimRight(a.endpoint, "/") + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(input.Data))
	if err != nil {
		return nil, fmt.Errorf("build R2 request: %w", err)
	}
	req.Header.Set("Content-Type", detectedMime)
	req.ContentLength = int64(len(input.Data))
	if requiresDownload(detectedMime) {
		req.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, storedName))
	}

	// Sign with AWS SigV4
	now := time.Now().UTC()
	if err := a.sign(req, input.Data, now, path); err != nil {
		return nil, fmt.Errorf("sign R2 request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("R2 put: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("R2 put returned status %d", resp.StatusCode)
	}

	sha := ComputeSHA256(input.Data)
	publicURL := strings.TrimRight(a.publicBaseURL, "/") + "/" + key

	return &StoredObject{
		StorageKey:      key,
		StoredFilename:  storedName,
		PublicURL:       publicURL,
		StorageProvider: "r2",
		MimeType:        detectedMime,
		DetectedMime:    detectedMime,
		SizeBytes:       int64(len(input.Data)),
		SHA256:          sha,
	}, nil
}

func (a *R2Adapter) Delete(ctx context.Context, storageKey string) error {
	path := "/" + a.bucket + "/" + storageKey
	url := strings.TrimRight(a.endpoint, "/") + path

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("build R2 delete request: %w", err)
	}
	now := time.Now().UTC()
	if err := a.sign(req, nil, now, path); err != nil {
		return fmt.Errorf("sign R2 delete: %w", err)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("R2 delete: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("R2 delete returned status %d", resp.StatusCode)
	}
	return nil
}

func (a *R2Adapter) Ping(ctx context.Context) error {
	path := "/" + a.bucket
	url := strings.TrimRight(a.endpoint, "/") + path

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_ = a.sign(req, nil, now, path)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("R2 ping returned status %d", resp.StatusCode)
	}
	return nil
}

// ─── AWS Signature Version 4 ─────────────────────────────────────────────────

const awsDateFormat = "20060102"
const awsDateTimeFormat = "20060102T150405Z"

func (a *R2Adapter) sign(req *http.Request, payload []byte, t time.Time, canonicalPath string) error {
	dateStr := t.Format(awsDateFormat)
	dateTimeStr := t.Format(awsDateTimeFormat)
	service := "s3"

	// Payload hash
	var payloadHash string
	if payload != nil {
		h := sha256.Sum256(payload)
		payloadHash = hex.EncodeToString(h[:])
	} else {
		h := sha256.Sum256([]byte{})
		payloadHash = hex.EncodeToString(h[:])
	}

	// Add required headers
	req.Header.Set("x-amz-date", dateTimeStr)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	hostHeader := req.URL.Host
	if hostHeader == "" {
		hostHeader = req.Host
	}

	// Canonical headers (sorted)
	headerMap := map[string]string{
		"host":                 hostHeader,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           dateTimeStr,
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headerMap["content-type"] = ct
	}
	headerKeys := make([]string, 0, len(headerMap))
	for k := range headerMap {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)

	var canonHeaders, signedHeaders strings.Builder
	for i, k := range headerKeys {
		canonHeaders.WriteString(k + ":" + headerMap[k] + "\n")
		if i > 0 {
			signedHeaders.WriteByte(';')
		}
		signedHeaders.WriteString(k)
	}

	// Canonical request
	query := ""
	if req.URL.RawQuery != "" {
		query = req.URL.RawQuery
	}
	canonReq := strings.Join([]string{
		req.Method,
		canonicalPath,
		query,
		canonHeaders.String(),
		signedHeaders.String(),
		payloadHash,
	}, "\n")

	// Credential scope
	credScope := strings.Join([]string{dateStr, a.region, service, "aws4_request"}, "/")

	// String to sign
	h := sha256.Sum256([]byte(canonReq))
	strToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		dateTimeStr,
		credScope,
		hex.EncodeToString(h[:]),
	}, "\n")

	// Signing key
	sigKey := deriveSigningKey(a.secretKey, dateStr, a.region, service)
	mac := hmac.New(sha256.New, sigKey)
	mac.Write([]byte(strToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	// Authorization header
	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		a.accessKeyID, credScope, signedHeaders.String(), signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
