// Package httpapi implements the HTTP server and all route handlers.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/benefit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/paidaccess"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/ratelimit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/renderer"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/searchindex"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/session"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/viewcount"
)

// BenefitRuntime holds all benefit-module dependencies.
// It is nil when BENEFIT_CLAIM_HMAC_SECRET is not configured.
type BenefitRuntime struct {
	Credential        *benefit.ClaimCredentialService
	Cipher            *benefit.SubscriptionCipher
	Turnstile         *benefit.TurnstileVerifier
	Opus8             *benefit.Opus8Client
	TurnstileSiteKey  string
	NetworkDailyLimit int
	ClaimMinuteLimit  int
}

// Server is the HTTP server with all dependencies wired in.
type Server struct {
	cfg         *config.Config
	repo        domain.Repository
	sessions    *session.Store
	limiter     ratelimit.Limiter
	storage     storage.Adapter
	localStore  *storage.LocalAdapter // nil if not local
	viewBuffer  *viewcount.Buffer
	searchCache *searchindex.Cache
	paidAccess  *paidaccess.Client
	benefit     *BenefitRuntime // nil = feature disabled
	logger      *slog.Logger
	mux         *http.ServeMux
}

// New creates a new Server with all dependencies.
func New(
	cfg *config.Config,
	repo domain.Repository,
	sessions *session.Store,
	limiter ratelimit.Limiter,
	stor storage.Adapter,
	localStore *storage.LocalAdapter,
	viewBuf *viewcount.Buffer,
	searchCache *searchindex.Cache,
	paidAccess *paidaccess.Client,
	benefitRuntime *BenefitRuntime,
	logger *slog.Logger,
) *Server {
	s := &Server{
		cfg:         cfg,
		repo:        repo,
		sessions:    sessions,
		limiter:     limiter,
		storage:     stor,
		localStore:  localStore,
		viewBuffer:  viewBuf,
		searchCache: searchCache,
		paidAccess:  paidAccess,
		benefit:     benefitRuntime,
		logger:      logger,
		mux:         http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler returns the HTTP handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = corsMiddleware(s.cfg.AllowedOrigins())(h)
	h = securityHeaders(h)
	h = requestLogger(s.logger)(h)
	h = recovery(s.logger)(h)
	h = requestIDMiddleware(h)
	return h
}

// routes registers all API endpoints.
func (s *Server) routes() {
	// Health
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /health/ready", s.healthReady)

	// ── Public read API ──────────────────────────────────────────────────────
	s.mux.HandleFunc("GET /api/posts", s.listPosts)
	s.mux.HandleFunc("GET /api/posts/{slug}", s.getPost)
	s.mux.HandleFunc("POST /api/posts/{slug}/view", s.recordView)
	s.mux.HandleFunc("GET /api/posts/{slug}/comments", s.listComments)
	s.mux.HandleFunc("POST /api/posts/{slug}/comments", s.createComment)
	s.mux.HandleFunc("POST /api/posts/{slug}/comment-attachments", s.uploadCommentAttachment)
	s.mux.HandleFunc("GET /api/products", s.listProducts)
	s.mux.HandleFunc("GET /api/products/{slug}", s.getProduct)
	s.mux.HandleFunc("GET /api/tools", s.listTools)
	s.mux.HandleFunc("GET /api/search-index", s.searchIndex)
	s.mux.HandleFunc("GET /api/uploads/{path...}", s.serveUpload)

	// ── Affiliate API ────────────────────────────────────────────────────────
	s.mux.HandleFunc("POST /api/affiliate/access", s.affiliateAccess)
	s.mux.HandleFunc("GET /api/affiliate/dashboard", s.affiliateDashboard)
	s.mux.HandleFunc("GET /api/affiliate/catalog", s.affiliateCatalog)
	s.mux.HandleFunc("PATCH /api/affiliate/markups", s.affiliateSetMarkups)
	s.mux.HandleFunc("POST /api/affiliate/clicks", s.affiliateRecordClick)
	s.mux.HandleFunc("POST /api/affiliate/logout", s.affiliateLogout)
	s.mux.HandleFunc("POST /api/orders", s.createOrder)

	// ── Admin API ────────────────────────────────────────────────────────────
	s.mux.HandleFunc("POST /api/admin/login", s.adminLogin)
	s.mux.HandleFunc("POST /api/admin/logout", s.adminLogout)
	s.mux.HandleFunc("GET /api/admin/session", s.adminGetSession)
	s.mux.HandleFunc("GET /api/admin/posts", s.adminListPosts)
	s.mux.HandleFunc("POST /api/admin/posts", s.adminCreatePost)
	s.mux.HandleFunc("PUT /api/admin/posts/{id}", s.adminUpdatePost)
	s.mux.HandleFunc("DELETE /api/admin/posts/{id}", s.adminDeletePost)
	s.mux.HandleFunc("POST /api/admin/attachments", s.adminUploadAttachment)
	s.mux.HandleFunc("GET /api/admin/products", s.adminListProducts)
	s.mux.HandleFunc("POST /api/admin/products", s.adminCreateProduct)
	s.mux.HandleFunc("PUT /api/admin/products/{id}", s.adminUpdateProduct)
	s.mux.HandleFunc("DELETE /api/admin/products/{id}", s.adminDeleteProduct)
	s.mux.HandleFunc("GET /api/admin/tools", s.adminListTools)
	s.mux.HandleFunc("POST /api/admin/tools", s.adminCreateTool)
	s.mux.HandleFunc("PUT /api/admin/tools/{id}", s.adminUpdateTool)
	s.mux.HandleFunc("DELETE /api/admin/tools/{id}", s.adminDeleteTool)
	s.mux.HandleFunc("GET /api/admin/affiliates", s.adminListAffiliates)
	s.mux.HandleFunc("PATCH /api/admin/affiliates/{id}", s.adminUpdateAffiliate)
	s.mux.HandleFunc("POST /api/admin/affiliates/{id}/reset-password", s.adminResetAffiliatePassword)
	s.mux.HandleFunc("GET /api/admin/affiliate-orders", s.adminListAffiliateOrders)
	s.mux.HandleFunc("PATCH /api/admin/affiliate-orders/{id}", s.adminUpdateAffiliateOrder)
	s.mux.HandleFunc("GET /api/admin/article-orders", s.adminListArticleOrders)
	s.mux.HandleFunc("PATCH /api/admin/article-orders/{id}", s.adminUpdateArticleOrder)
	s.mux.HandleFunc("GET /api/admin/reader-accounts", s.adminListReaderAccounts)
	s.mux.HandleFunc("POST /api/admin/reader-accounts/{id}/reset-password", s.adminResetReaderPassword)

	// ── Benefit API ──────────────────────────────────────────────────────────
	s.mux.HandleFunc("GET /api/benefits/webmaster", s.benefitInfo)
	s.mux.HandleFunc("GET /api/benefits/webmaster/claim", s.benefitStatus)
	s.mux.HandleFunc("POST /api/benefits/webmaster/claim", s.benefitClaim)
	s.mux.HandleFunc("GET /api/benefits/webmaster/status", s.benefitStatus)
	s.mux.HandleFunc("POST /api/benefits/webmaster/provision", s.benefitProvision)
}

// ─── Health checks ────────────────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) healthReady(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	allOk := true

	if err := s.repo.Ping(r.Context()); err != nil {
		s.logger.Error("readiness postgres check failed", "error", err)
		checks["postgres"] = "unhealthy"
		allOk = false
	} else {
		checks["postgres"] = "ok"
	}

	if err := s.sessions.Ping(r.Context()); err != nil {
		s.logger.Warn("readiness redis check failed", "error", err)
		checks["redis"] = "degraded" // Redis failure ≠ unhealthy
	} else {
		checks["redis"] = "ok"
	}

	status := http.StatusOK
	if !allOk {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": allOk, "checks": checks})
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("request failed",
		"request_id", requestIDFromRequest(r),
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时不可用")
}

// remoteIP extracts the client IP, respecting X-Forwarded-For when TrustProxy is set.
func (s *Server) remoteIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
	}
	// Strip port
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// renderMarkdown renders markdown using the renderer package.
func renderMarkdown(markdown string) renderer.Result {
	return renderer.Render(markdown)
}

// ─── JSON helpers (shared across handler files) ───────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// flattenedResponse preserves both the historical top-level write response
// and the newer nested object used by integration clients.
func flattenedResponse(key string, value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{key: value}
	}
	response := make(map[string]any)
	if err := json.Unmarshal(data, &response); err != nil {
		return map[string]any{key: value}
	}
	response[key] = value
	return response
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 256*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求格式不正确")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求格式不正确")
		return false
	}
	return true
}
