// Package mediaimport retrieves a remote article image without trusting the
// source URL. It deliberately has no access to the caller's cookies or other
// request credentials.
package mediaimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

const (
	defaultMaxBytes int64 = 20 * 1024 * 1024
	defaultTimeout        = 15 * time.Second
	maxRedirects          = 3
)

// Failure is a bounded, user-actionable import failure. The wrapped error is
// retained for server logs only; callers should return Code/Message/Resolution.
type Failure struct {
	Code       string
	Message    string
	Resolution string
	err        error
}

func (e *Failure) Error() string {
	if e.err == nil {
		return e.Code
	}
	return e.Code + ": " + e.err.Error()
}

func (e *Failure) Unwrap() error { return e.err }

// Result is a validated image ready for the project's storage adapter.
type Result struct {
	Data     []byte
	MimeType string
	Host     string
}

// Resolver is deliberately injectable so the SSRF policy has deterministic
// tests and is applied at URL validation as well as immediately before dialing.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type defaultResolver struct{}

func (defaultResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

// Importer performs bounded remote fetches. Zero values choose safe defaults.
type Importer struct {
	MaxBytes  int64
	Timeout   time.Duration
	Resolver  Resolver
	newClient func(timeout time.Duration, resolver Resolver) *http.Client
}

// Fetch downloads a single image from an HTTPS public host. Every redirect and
// every DNS resolution is revalidated to resist DNS rebinding and redirect SSRF.
func (i Importer) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	maxBytes := i.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	timeout := i.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	resolver := i.Resolver
	if resolver == nil {
		resolver = defaultResolver{}
	}

	parsed, err := parsePublicHTTPSURL(ctx, resolver, rawURL)
	if err != nil {
		return nil, err
	}

	clientFactory := i.newClient
	if clientFactory == nil {
		clientFactory = newSafeHTTPClient
	}
	client := clientFactory(timeout, resolver)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, &Failure{Code: "INVALID_SOURCE_URL", Message: "图片地址无效", Resolution: "请重新粘贴图片，或上传本地图片", err: err}
	}
	// Do not copy headers from the admin request. In particular, no Cookie,
	// Authorization or Referer is sent to the third-party source.
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*;q=0.8")
	req.Header.Set("User-Agent", "FreedomPost-Image-Importer/1.0")

	response, err := client.Do(req)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return nil, failure
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Failure{Code: "SOURCE_TIMEOUT", Message: "图片来源响应超时", Resolution: "请稍后重试，或下载图片后上传本地文件", err: err}
		}
		return nil, &Failure{Code: "SOURCE_UNREACHABLE", Message: "无法读取图片来源", Resolution: "请确认图片仍可公开访问；需要登录或防盗链的图片请上传本地文件", err: err}
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &Failure{Code: "SOURCE_HTTP_ERROR", Message: "图片来源拒绝了下载", Resolution: "请确认图片链接仍有效且无需登录；也可以上传本地图片", err: fmt.Errorf("source returned HTTP %d", response.StatusCode)}
	}
	if response.ContentLength > maxBytes {
		return nil, &Failure{Code: "IMAGE_TOO_LARGE", Message: "图片超过允许的大小", Resolution: "请压缩图片后重新粘贴，或上传较小的本地文件", err: fmt.Errorf("content length %d exceeds %d", response.ContentLength, maxBytes)}
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, &Failure{Code: "SOURCE_READ_ERROR", Message: "读取图片时失败", Resolution: "请稍后重试，或上传本地图片", err: err}
	}
	if int64(len(data)) > maxBytes {
		return nil, &Failure{Code: "IMAGE_TOO_LARGE", Message: "图片超过允许的大小", Resolution: "请压缩图片后重新粘贴，或上传较小的本地文件"}
	}
	mimeType, err := storage.Validate(data, maxBytes)
	if err != nil || !strings.HasPrefix(mimeType, "image/") || mimeType == "image/svg+xml" {
		return nil, &Failure{Code: "UNSUPPORTED_IMAGE", Message: "来源内容不是受支持的图片", Resolution: "请使用 JPEG、PNG、GIF、WebP 或 AVIF 图片，或上传本地图片", err: err}
	}
	return &Result{Data: data, MimeType: mimeType, Host: parsed.Hostname()}, nil
}

func newSafeHTTPClient(timeout time.Duration, resolver Resolver) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil, // a configured proxy could otherwise fetch an internal URL.
		DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			if port != "443" {
				return nil, fmt.Errorf("unexpected remote port %q", port)
			}
			addresses, lookupErr := resolvePublicHost(dialCtx, resolver, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			var lastErr error
			for _, address := range addresses {
				conn, err := dialer.DialContext(dialCtx, network, net.JoinHostPort(address.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return &Failure{Code: "TOO_MANY_REDIRECTS", Message: "图片来源重定向次数过多", Resolution: "请上传本地图片，或使用不需要多次跳转的 HTTPS 图片链接"}
			}
			_, err := parsePublicHTTPSURL(request.Context(), resolver, request.URL.String())
			return err
		},
	}
}

func parsePublicHTTPSURL(ctx context.Context, resolver Resolver, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, &Failure{Code: "INVALID_SOURCE_URL", Message: "图片地址无效", Resolution: "请重新粘贴图片，或上传本地图片", err: err}
	}
	if parsed.Scheme != "https" {
		return nil, &Failure{Code: "HTTPS_REQUIRED", Message: "只能转存 HTTPS 图片", Resolution: "请使用 HTTPS 图片链接，或上传本地图片"}
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, &Failure{Code: "UNSAFE_SOURCE", Message: "图片地址使用了不受支持的端口", Resolution: "请使用标准 HTTPS 图片地址，或上传本地图片"}
	}
	if _, err := resolvePublicHost(ctx, resolver, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolvePublicHost(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	if host == "" {
		return nil, &Failure{Code: "INVALID_SOURCE_URL", Message: "图片地址缺少主机名", Resolution: "请重新粘贴图片，或上传本地图片"}
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddress(literal) {
			return nil, unsafeAddressFailure(literal.String())
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, &Failure{Code: "SOURCE_UNREACHABLE", Message: "无法解析图片来源", Resolution: "请确认图片链接仍有效，或上传本地图片", err: err}
	}
	if len(addresses) == 0 {
		return nil, &Failure{Code: "SOURCE_UNREACHABLE", Message: "无法解析图片来源", Resolution: "请确认图片链接仍有效，或上传本地图片"}
	}
	public := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicAddress(address) {
			return nil, unsafeAddressFailure(address.String())
		}
		public = append(public, address)
	}
	return public, nil
}

func unsafeAddressFailure(_ string) error {
	return &Failure{Code: "UNSAFE_SOURCE", Message: "图片来源地址不允许转存", Resolution: "请使用公开的 HTTPS 图片，或上传本地图片"}
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() || address.IsPrivate() {
		return false
	}
	if address.Is4() {
		octets := address.As4()
		if octets[0] == 0 || octets[0] >= 224 ||
			(octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127) ||
			(octets[0] == 192 && octets[1] == 0 && octets[2] == 2) ||
			(octets[0] == 198 && (octets[1] == 18 || octets[1] == 19)) ||
			(octets[0] == 198 && octets[1] == 51 && octets[2] == 100) ||
			(octets[0] == 203 && octets[1] == 0 && octets[2] == 113) {
			return false
		}
	}
	if address.Is6() {
		octets := address.As16()
		if octets[0] == 0x20 && octets[1] == 0x01 && octets[2] == 0x0d && octets[3] == 0xb8 { // 2001:db8::/32 documentation range
			return false
		}
	}
	return true
}

// Filename returns a harmless storage filename independent of the remote URL.
func Filename(name, mimeType string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "imported-image"
	}
	if len(name) > 128 {
		name = name[:128]
	}
	if strings.ContainsAny(name, `/\\`) {
		name = "imported-image"
	}
	if strings.Contains(name, ".") {
		return name
	}
	return name + imageExtension(mimeType)
}

func imageExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	default:
		return ".img"
	}
}
