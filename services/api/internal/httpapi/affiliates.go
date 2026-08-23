package httpapi

import (
	"net/http"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

// ─── Affiliate auth ───────────────────────────────────────────────────────────

// affiliateAccess handles both login and auto-registration.
// Matches the TypeScript /api/affiliate/access endpoint exactly.
func (s *Server) affiliateAccess(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	ipKey := security.HashText(ip)

	// Rate limit: 10 attempts per 15 minutes per IP
	if ok, _ := s.limiter.Allow(r.Context(), "affiliate-login:"+ipKey, 10, 15*time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "操作太频繁，请稍后再试")
		return
	}

	var input struct {
		WechatID string `json:"wechatId"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.WechatID == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "微信号和密码不能为空")
		return
	}

	ctx := r.Context()

	// Look up existing affiliate
	affiliate, err := s.repo.GetAffiliateByWechatID(ctx, input.WechatID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	if affiliate == nil {
		// Auto-register: hash the password and create
		hash, err := bcryptHashReal(input.Password)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		affiliate, err = s.repo.CreateAffiliate(ctx, input.WechatID, hash)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	} else {
		// Verify password
		if err := bcryptVerifyReal(affiliate.PasswordHash, input.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "LOGIN_FAILED", "微信号或密码不正确")
			return
		}
		// Check active status
		if affiliate.Status != domain.AffiliateStatusActive {
			writeError(w, http.StatusForbidden, "ACCOUNT_INACTIVE", "账号已被暂停，请联系站长")
			return
		}
	}

	// Issue session
	token, err := security.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sess := domain.AffiliateSession{
		AffiliateID: affiliate.ID,
		WechatID:    affiliate.WechatID,
		CreatedAt:   time.Now(),
	}
	if err := s.sessions.SetAffiliateSession(ctx, security.HashToken(token), sess); err != nil {
		s.internalError(w, r, err)
		return
	}

	setSessionCookie(w, affiliateCookieName, token, int((30 * 24 * time.Hour).Seconds()), s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *Server) affiliateLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(affiliateCookieName); err == nil {
		_ = s.sessions.DeleteAffiliateSession(r.Context(), security.HashToken(cookie.Value))
	}
	clearCookie(w, affiliateCookieName, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── Affiliate portal ─────────────────────────────────────────────────────────

func (s *Server) affiliateDashboard(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAffiliate(w, r)
	if sess == nil {
		return
	}
	dashboard, err := s.repo.GetAffiliateDashboard(r.Context(), sess.AffiliateID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboard": dashboard})
}

func (s *Server) affiliateCatalog(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAffiliate(w, r)
	if sess == nil {
		return
	}
	products, err := s.repo.ListAffiliateProducts(r.Context(), sess.AffiliateID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": products})
}

func (s *Server) affiliateSetMarkups(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAffiliate(w, r)
	if sess == nil {
		return
	}
	var input struct {
		ProductIDs    []string `json:"productIds"` // nil = set default
		MarkupPercent int      `json:"markupPercent"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.repo.SetAffiliateMarkup(r.Context(), sess.AffiliateID, input.ProductIDs, input.MarkupPercent); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) affiliateRecordClick(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WechatID   string `json:"wechatId"`
		VisitorKey string `json:"visitorKey"`
		Path       string `json:"path"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	// Derive visitor key from IP if not provided
	if input.VisitorKey == "" {
		ip := s.remoteIP(r)
		input.VisitorKey = security.HashVisitorKey(ip, s.cfg.VisitorHashSalt)
	}

	accepted, isUnique, err := s.repo.RecordAffiliateClick(r.Context(), input.WechatID, input.VisitorKey, input.Path)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted, "isUnique": isUnique})
}

// ─── Orders ───────────────────────────────────────────────────────────────────

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAffiliate(w, r)
	if sess == nil {
		return
	}

	var input struct {
		ProductID       string `json:"productId"`
		CustomerContact string `json:"customerContact"`
		MarkupPercent   int    `json:"markupPercent"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	product, err := s.repo.GetProductByID(r.Context(), input.ProductID)
	if err != nil || product == nil {
		writeError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if product.Status != "published" {
		writeError(w, http.StatusBadRequest, "PRODUCT_NOT_PUBLISHED", "商品未上架")
		return
	}

	// Compute server-side pricing (never trust client amounts)
	view := domain.BuildAffiliateProductView(*product, input.MarkupPercent)

	order, err := s.repo.CreateAffiliateOrder(r.Context(), domain.CreateOrderInput{
		AffiliateID:           sess.AffiliateID,
		ProductID:             product.ID,
		CustomerContact:       input.CustomerContact,
		CustomerPriceCents:    view.CustomerPriceCents,
		CommissionCents:       view.CommissionCents,
		BaseCommissionCents:   view.BaseCommissionCents,
		MarkupCommissionCents: view.MarkupCommissionCents,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order": order})
}
