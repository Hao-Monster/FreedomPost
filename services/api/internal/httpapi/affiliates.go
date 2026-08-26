package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/google/uuid"
)

var affiliateWechatIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{5,31}$`)

// ─── Affiliate auth ───────────────────────────────────────────────────────────

// affiliateAccess handles both login and auto-registration.
// Matches the TypeScript /api/affiliate/access endpoint exactly.
func (s *Server) affiliateAccess(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	ipKey := security.HashText(ip)

	if ok, _ := s.limiter.Allow(r.Context(), "affiliate-login:"+ipKey, 12, time.Minute); !ok {
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
	input.WechatID = normalizeAffiliateWechatID(input.WechatID)
	if input.WechatID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_WECHAT_ID", "微信号格式不正确")
		return
	}

	ctx := r.Context()

	// Look up existing affiliate
	affiliate, err := s.repo.GetAffiliateByWechatID(ctx, input.WechatID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	created := false
	generatedPassword := ""
	if affiliate == nil {
		created = true
		generatedPassword, err = generateAffiliatePassword()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		hash, err := bcryptHashReal(generatedPassword)
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
		if affiliate.Status != domain.AffiliateStatusActive {
			writeError(w, http.StatusForbidden, "AFFILIATE_DISABLED", "该推广账号已停用")
			return
		}
		if !validAffiliatePassword(input.Password) {
			writeError(w, http.StatusUnauthorized, "PASSWORD_REQUIRED", "请输入查询密码")
			return
		}
		if err := bcryptVerifyReal(affiliate.PasswordHash, input.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "INVALID_PASSWORD", "微信号或查询密码不正确")
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
		AffiliateID:       affiliate.ID,
		WechatID:          affiliate.WechatID,
		CredentialVersion: affiliate.CredentialVersion,
		CreatedAt:         time.Now(),
	}
	if err := s.sessions.SetAffiliateSession(ctx, security.HashToken(token), sess); err != nil {
		s.internalError(w, r, err)
		return
	}

	setSessionCookie(w, affiliateCookieName, token, int((30 * 24 * time.Hour).Seconds()), s.cfg.CookieSecure)
	dashboard, err := s.repo.GetAffiliateDashboard(ctx, affiliate.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "generatedPassword": generatedPassword,
		"shareUrl":  s.affiliateShareURL(affiliate.WechatID),
		"dashboard": dashboard, "session": sess,
	})
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
	writeJSON(w, http.StatusOK, map[string]any{"shareUrl": s.affiliateShareURL(sess.WechatID), "dashboard": dashboard})
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
	writeJSON(w, http.StatusOK, map[string]any{"items": products, "products": products})
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
	if !domain.ValidAffiliateMarkupPercent(input.MarkupPercent) {
		writeError(w, http.StatusBadRequest, "INVALID_MARKUP", "加价比例必须在 0 到 1000 之间")
		return
	}
	if input.ProductIDs != nil {
		if len(input.ProductIDs) > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_PRODUCTS", "单次最多设置 100 个商品")
			return
		}
		seen := make(map[string]struct{}, len(input.ProductIDs))
		for _, productID := range input.ProductIDs {
			if uuid.Validate(productID) != nil {
				writeError(w, http.StatusBadRequest, "INVALID_PRODUCTS", "商品标识格式不正确")
				return
			}
			if _, exists := seen[productID]; exists {
				writeError(w, http.StatusBadRequest, "INVALID_PRODUCTS", "商品不能重复提交")
				return
			}
			seen[productID] = struct{}{}
		}
	}
	if err := s.repo.SetAffiliateMarkup(r.Context(), sess.AffiliateID, input.ProductIDs, input.MarkupPercent); err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) {
			writeError(w, http.StatusBadRequest, "INVALID_PRODUCTS", "商品或加价设置无效")
			return
		}
		s.internalError(w, r, err)
		return
	}
	products, err := s.repo.ListAffiliateProducts(r.Context(), sess.AffiliateID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": products, "products": products})
}

func (s *Server) affiliateRecordClick(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	if ok, _ := s.limiter.Allow(r.Context(), "affiliate-click:"+security.HashText(ip), 120, time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "点击记录过于频繁")
		return
	}
	var input struct {
		Ref     string `json:"ref"`
		LocalID string `json:"localId"`
		Path    string `json:"path"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	input.Ref = normalizeAffiliateWechatID(input.Ref)
	input.LocalID = strings.TrimSpace(input.LocalID)
	input.Path = strings.TrimSpace(input.Path)
	if input.Path == "" {
		input.Path = "/market/"
	}
	if input.Ref == "" || input.LocalID == "" || len(input.LocalID) > 128 || len(input.Path) > 500 || !strings.HasPrefix(input.Path, "/") || strings.HasPrefix(input.Path, "//") {
		writeError(w, http.StatusBadRequest, "INVALID_REFERRAL", "推广链接无效")
		return
	}
	visitorKey := security.HashText(input.LocalID + ":" + ip + ":" + r.UserAgent() + ":" + s.cfg.VisitorHashSalt)
	accepted, isUnique, err := s.repo.RecordAffiliateClick(r.Context(), input.Ref, visitorKey, input.Path)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !accepted {
		writeError(w, http.StatusNotFound, "AFFILIATE_NOT_FOUND", "推广链接无效或已停用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "isUnique": isUnique, "ref": input.Ref})
}

// ─── Orders ───────────────────────────────────────────────────────────────────

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	if ok, _ := s.limiter.Allow(r.Context(), "affiliate-order:"+security.HashText(ip), 20, time.Hour); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "下单次数过多，请稍后再试")
		return
	}

	var input struct {
		ProductSlug         string `json:"productSlug"`
		RecommenderWechatID string `json:"recommenderWechatId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	input.ProductSlug = strings.TrimSpace(input.ProductSlug)
	input.RecommenderWechatID = normalizeAffiliateWechatID(input.RecommenderWechatID)
	if input.ProductSlug == "" || len(input.ProductSlug) > 64 || input.RecommenderWechatID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ORDER", "商品或推荐人微信号格式不正确")
		return
	}

	order, err := s.repo.CreateAffiliateOrder(r.Context(), domain.CreateOrderInput{
		AffiliateWechatID: input.RecommenderWechatID,
		ProductSlug:       input.ProductSlug,
	})
	if errors.Is(err, domain.ErrInvalidRecommender) {
		writeError(w, http.StatusBadRequest, "INVALID_RECOMMENDER", "推荐人微信号不存在或已停用")
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "PRODUCT_UNAVAILABLE", "商品不存在、已售罄或推荐人无效")
		return
	}
	if errors.Is(err, domain.ErrInvalidState) {
		writeError(w, http.StatusConflict, "INVALID_PRICING", "商品定价状态无效")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order": order})
}

func normalizeAffiliateWechatID(value string) string {
	value = strings.TrimSpace(value)
	if !affiliateWechatIDPattern.MatchString(value) {
		return ""
	}
	return value
}

func generateAffiliatePassword() (string, error) {
	token, err := security.NewToken()
	if err != nil {
		return "", err
	}
	return strings.ToUpper(token[:12]), nil
}

func (s *Server) affiliateShareURL(wechatID string) string {
	return strings.TrimRight(s.cfg.PublicSiteURL, "/") + "/market/?ref=" + url.QueryEscape(wechatID)
}
