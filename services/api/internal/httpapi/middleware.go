package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

// requestIDMiddleware creates a server-authoritative identifier so an error
// shown in the admin UI can be correlated with the corresponding server logs.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := uuid.NewString()
		w.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func requestIDFromRequest(r *http.Request) string {
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	return requestID
}

// ─── Recovery middleware ──────────────────────────────────────────────────────

func recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					buf := make([]byte, 8192)
					n := runtime.Stack(buf, false)
					logger.Error("panic recovered",
						"request_id", requestIDFromRequest(r),
						"panic", rec,
						"stack", string(buf[:n]),
						"method", r.Method,
						"path", r.URL.Path,
					)
					writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时不可用")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Request logger ───────────────────────────────────────────────────────────

// sensitivePathPrefixes lists paths whose request bodies should not be logged.
var sensitivePathPrefixes = []string{
	"/api/admin/login",
	"/api/affiliate/access",
}

func requestLogger(logger *slog.Logger, trustProxy ...bool) func(http.Handler) http.Handler {
	trusted := len(trustProxy) > 0 && trustProxy[0]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)

			// Hash IP for logs (never log raw IP)
			ip := realIP(r, trusted)
			logger.Info("request",
				"request_id", requestIDFromRequest(r),
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"ip_prefix", maskIP(ip), // last octet masked
			)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

var cloudflareTrustedNets = mustParseCIDRs([]string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
})

func mustParseCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		nets = append(nets, ipNet)
	}
	return nets
}

func isCloudflareProxyIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, ipNet := range cloudflareTrustedNets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func singleValidHeaderIP(values []string) net.IP {
	var found net.IP
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, ",") || found != nil {
			return nil
		}
		parsed := net.ParseIP(trimmed)
		if parsed == nil {
			return nil
		}
		found = parsed
	}
	return found
}

func realIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		values := r.Header.Values("X-Forwarded-For")
		for i := len(values) - 1; i >= 0; i-- {
			if raw := strings.TrimSpace(values[i]); raw != "" {
				parts := strings.Split(raw, ",")
				last := strings.TrimSpace(parts[len(parts)-1])
				if parsed := net.ParseIP(last); parsed != nil {
					if isCloudflareProxyIP(parsed) {
						if cfIP := singleValidHeaderIP(r.Header.Values("CF-Connecting-IP")); cfIP != nil {
							return cfIP.String()
						}
						if len(parts) >= 2 {
							if prev := net.ParseIP(strings.TrimSpace(parts[len(parts)-2])); prev != nil {
								return prev.String()
							}
						}
					}
					return parsed.String()
				}
				break
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if parsed := net.ParseIP(host); parsed != nil {
			return parsed.String()
		}
		return host
	}
	if parsed := net.ParseIP(strings.Trim(r.RemoteAddr, "[]")); parsed != nil {
		return parsed.String()
	}
	return strings.Trim(r.RemoteAddr, "[]")
}

func maskIP(ip string) string {
	if len(ip) == 0 {
		return ""
	}
	// For IPv4: mask last octet. For IPv6: show /48 prefix.
	if parsed := net.ParseIP(ip); parsed != nil {
		if v4 := parsed.To4(); v4 != nil {
			return net.IP{v4[0], v4[1], v4[2], 0}.String() + ".x"
		}
	}
	// IPv6: show first 6 bytes
	if len(ip) > 12 {
		return ip[:12] + "..."
	}
	return ip
}

func splitAndTrim(s, sep string) []string {
	parts := make([]string, 0)
	for _, p := range splitStr(s, sep) {
		if t := trimStr(p); t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}

func splitStr(s, sep string) []string {
	result := []string{}
	for {
		i := indexOf(s, sep)
		if i < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:i])
		s = s[i+len(sep):]
	}
	return result
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimStr(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// ─── Security headers ─────────────────────────────────────────────────────────
// Aligned with paid-access securityHeaders middleware.

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ─── CORS middleware ──────────────────────────────────────────────────────────
// Aligned with paid-access validOrigin() logic.

func corsMiddleware(allowedOrigins map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if allowedOrigins[origin] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Order-CSRF")
					w.Header().Set("Access-Control-Expose-Headers", requestIDHeader)
					w.Header().Set("Vary", "Origin")
				}
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// adminOriginProtection covers browser write routes only. Signed integration
// callbacks and internal service endpoints do not rely on browser Origin.
func adminOriginProtection(allowedOrigins map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if browserWritePath(r.URL.Path) &&
				r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				origin := r.Header.Get("Origin")
				if r.Header.Get("Sec-Fetch-Site") == "cross-site" || origin == "" || !allowedOrigins[origin] {
					writeError(w, http.StatusForbidden, "ORIGIN_FORBIDDEN", "请求来源不被允许")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Cookie helpers ───────────────────────────────────────────────────────────

func browserWritePath(path string) bool {
	return strings.HasPrefix(path, "/api/admin/") || strings.HasPrefix(path, "/api/affiliate/") ||
		path == "/api/orders" || strings.HasPrefix(path, "/api/orders/") ||
		strings.HasPrefix(path, "/api/posts/") || strings.HasPrefix(path, "/api/benefits/")
}

const (
	adminCookieName     = "fp_admin_session"
	affiliateCookieName = "fp_affiliate_session"
	readerCookieName    = "fp_reader_session"
)

func setSessionCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
