package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime"
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

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)

			// Hash IP for logs (never log raw IP)
			ip := realIP(r)
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

func realIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := splitAndTrim(fwd, ",")
		if len(parts) > 0 {
			return parts[0]
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
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
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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

// ─── Cookie helpers ───────────────────────────────────────────────────────────

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
