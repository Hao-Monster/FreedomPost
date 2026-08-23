package httpapi

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/benefit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

// ─── Webmaster Benefit Policy ─────────────────────────────────────────────────
// These constants MUST stay in sync with TS policy.ts and Opus8 server-side
// expectations. Do NOT change without coordinating with Opus8 team.

const (
	webmasterCampaignID = "webmaster-benefit-v1"
	webmasterTraffic    = int64(30 * 1024 * 1024 * 1024) // 30 GiB
	webmasterDuration   = 15                             // days
	webmasterHWID       = true
	webmasterIPLimit    = 2
)

const benefitCookieName = "fp_benefit_claim"

// noStoreBenefit sets cache-prevention headers for benefit endpoints.
func noStoreBenefit(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// ─── GET /api/benefits/webmaster ─────────────────────────────────────────────

// benefitInfo returns campaign metadata and issues a claim credential cookie
// if the visitor doesn't have one yet.
func (s *Server) benefitInfo(w http.ResponseWriter, r *http.Request) {
	noStoreBenefit(w)

	campaign, err := s.repo.GetBenefitCampaign(r.Context(), webmasterCampaignID)
	if err != nil {
		s.logger.Warn("benefit: get campaign failed", "error", err)
	}
	enabled := s.benefit != nil && campaignIsActive(campaign)

	// Issue a browser credential cookie if visitor doesn't have one.
	if s.benefit != nil {
		cookieVal := ""
		if c, err := r.Cookie(benefitCookieName); err == nil {
			cookieVal = c.Value
		}
		if _, ok := s.benefit.Credential.Verify(cookieVal); !ok {
			newVal, _, issueErr := s.benefit.Credential.Issue()
			if issueErr == nil {
				http.SetCookie(w, &http.Cookie{
					Name:     benefitCookieName,
					Value:    newVal,
					Path:     "/",
					MaxAge:   s.benefit.Credential.CookieTTL(),
					HttpOnly: true,
					Secure:   s.cfg.CookieSecure,
					SameSite: http.SameSiteLaxMode,
				})
			}
		}
	}

	var siteKey *string
	if s.benefit != nil {
		k := s.benefit.TurnstileSiteKey
		siteKey = &k
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":               webmasterCampaignID,
		"enabled":          enabled,
		"trafficBytes":     webmasterTraffic,
		"durationDays":     webmasterDuration,
		"hwidRequired":     webmasterHWID,
		"ipLimit":          webmasterIPLimit,
		"turnstileSiteKey": siteKey,
	})
}

// ─── POST /api/benefits/webmaster/claim ──────────────────────────────────────

// benefitClaim handles the full claim flow:
//  1. Validate campaign is active
//  2. Verify Turnstile token
//  3. Check per-network daily rate limit
//  4. Create or resume a claim record
//  5. Call Opus8 to provision the subscription
//  6. Return the encrypted subscription URL
func (s *Server) benefitClaim(w http.ResponseWriter, r *http.Request) {
	noStoreBenefit(w)
	if s.benefit == nil {
		writeError(w, http.StatusServiceUnavailable, "BENEFIT_UNAVAILABLE", "Benefit 功能暂不可用")
		return
	}
	b := s.benefit

	// 1. Campaign active?
	campaign, _ := s.repo.GetBenefitCampaign(r.Context(), webmasterCampaignID)
	if !campaignIsActive(campaign) {
		writeError(w, http.StatusForbidden, "BENEFIT_DISABLED", "活动暂未开放")
		return
	}

	// 2. Parse + validate request body
	var body struct {
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.TurnstileToken) < 1 || len(body.TurnstileToken) > 2_048 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "无效的 Turnstile token")
		return
	}

	// 3. Credential cookie
	cookieVal := ""
	if c, err := r.Cookie(benefitCookieName); err == nil {
		cookieVal = c.Value
	}
	browserKeyHash, ok := b.Credential.Verify(cookieVal)
	if !ok {
		writeError(w, http.StatusForbidden, "CLAIM_CREDENTIAL_REQUIRED", "请先刷新页面")
		return
	}

	// 4. Check existing claim
	claim, _ := s.repo.GetBenefitClaimByBrowserKey(r.Context(), webmasterCampaignID, browserKeyHash)
	if claim != nil {
		switch claim.Status {
		case "ready":
			sendBenefitReady(w, claim, b)
			return
		case "revoked", "expired":
			writeError(w, http.StatusGone, "BENEFIT_CLAIM_UNAVAILABLE", "该福利已不可用")
			return
		}
	}

	// 5. Per-minute rate limit (network key = IP hash)
	networkKeyHash := b.Credential.HashNetworkKey(s.remoteIP(r))
	allowed, err := s.limiter.Allow(r.Context(),
		"benefit-claim-minute:"+networkKeyHash, b.ClaimMinuteLimit, time.Minute)
	if err != nil {
		s.logger.Warn("benefit: rate limit check failed", "error", err)
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁，请稍后再试")
		return
	}

	// 6. Verify Turnstile
	result := b.Turnstile.Verify(r.Context(), body.TurnstileToken, s.remoteIP(r))
	if !result.Valid {
		code := "TURNSTILE_REJECTED"
		status := http.StatusForbidden
		if result.Retryable {
			code = "TURNSTILE_UNAVAILABLE"
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, code, "人机验证失败，请重试")
		return
	}

	// 7. Network daily limit check
	sinceStr := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	recentCount, _ := s.repo.CountBenefitClaimsByNetworkSince(r.Context(), webmasterCampaignID, networkKeyHash, sinceStr)
	if recentCount >= b.NetworkDailyLimit {
		writeError(w, http.StatusTooManyRequests, "NETWORK_DAILY_LIMIT", "今日该网络已达领取上限")
		return
	}

	// 8. Create claim if not exists
	isNew := false
	if claim == nil {
		created, createErr := s.repo.CreateBenefitClaim(r.Context(), domain.CreateBenefitClaimInput{
			CampaignID:      webmasterCampaignID,
			ExternalClaimID: newBenefitClaimID(),
			BrowserKeyHash:  browserKeyHash,
			NetworkKeyHash:  networkKeyHash,
		})
		if createErr != nil {
			s.internalError(w, r, createErr)
			return
		}
		claim = created.Claim
		isNew = created.Created
	}

	// 9. Handle stale provisioning (recover if > 30s old)
	if claim.Status == "provisioning" {
		recoveredClaim, _ := s.repo.RecoverStaleBenefitProvisioning(r.Context(), claim.ID,
			time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339))
		if recoveredClaim == nil {
			w.Header().Set("Retry-After", "3")
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status": "provisioning", "retryAfterSeconds": 3,
			})
			return
		}
		claim = recoveredClaim
	}

	// 10. Begin provisioning (optimistic lock)
	provisioning, _ := s.repo.BeginBenefitProvisioning(r.Context(), claim.ID)
	if provisioning == nil {
		// Race — another request won
		current, _ := s.repo.GetBenefitClaimByID(r.Context(), claim.ID)
		if current != nil && current.Status == "ready" {
			sendBenefitReady(w, current, b)
			return
		}
		w.Header().Set("Retry-After", "3")
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "provisioning", "retryAfterSeconds": 3})
		return
	}

	// 11. Call Opus8
	upstream, opus8Err := b.Opus8.ClaimWebmasterBenefit(r.Context(), provisioning.ExternalClaimID)
	if opus8Err != nil {
		errCode := "benefit_provisioning_failed"
		if oe, ok := opus8Err.(*benefit.Opus8Error); ok {
			if len(oe.Code) > 0 && len(oe.Code) <= 64 {
				errCode = oe.Code
			}
		}
		_ = s.repo.FailBenefitClaim(r.Context(), provisioning.ID, errCode)
		s.logger.Warn("benefit: opus8 provisioning failed", "code", errCode, "error", opus8Err)
		writeError(w, http.StatusServiceUnavailable, "BENEFIT_PROVISIONING_UNAVAILABLE", "服务暂时不可用，请稍后重试")
		return
	}

	// 12. Encrypt subscription URL
	encURL, encErr := b.Cipher.Encrypt(upstream.SubscriptionURL, benefit.SubscriptionCipherAAD{
		CampaignID: provisioning.CampaignID,
		ClaimID:    provisioning.ID,
	})
	if encErr != nil {
		_ = s.repo.FailBenefitClaim(r.Context(), provisioning.ID, "encryption_failed")
		s.internalError(w, r, encErr)
		return
	}

	// 13. Complete claim
	completed, completeErr := s.repo.CompleteBenefitClaim(r.Context(), provisioning.ID, domain.CompleteBenefitClaimInput{
		OpusUserID:         upstream.OpusUserID,
		OpusDeviceID:       upstream.OpusDeviceID,
		SubscriptionURLEnc: encURL,
		ExpiresAt:          upstream.ExpiresAt,
	})
	if completeErr != nil || completed == nil {
		writeError(w, http.StatusConflict, "CLAIM_STATE_CONFLICT", "状态冲突，请重试")
		return
	}

	statusCode := http.StatusOK
	if isNew {
		statusCode = http.StatusCreated
	}
	sendBenefitReady(w, completed, b)
	_ = statusCode // status code is set in sendBenefitReady
}

// ─── GET /api/benefits/webmaster/claim ───────────────────────────────────────

// benefitStatus returns the current claim status for the authenticated browser.
func (s *Server) benefitStatus(w http.ResponseWriter, r *http.Request) {
	noStoreBenefit(w)
	if s.benefit == nil {
		writeError(w, http.StatusServiceUnavailable, "BENEFIT_UNAVAILABLE", "Benefit 功能暂不可用")
		return
	}

	cookieVal := ""
	if c, err := r.Cookie(benefitCookieName); err == nil {
		cookieVal = c.Value
	}
	browserKeyHash, ok := s.benefit.Credential.Verify(cookieVal)
	if !ok {
		writeError(w, http.StatusNotFound, "BENEFIT_CLAIM_NOT_FOUND", "未找到领取记录")
		return
	}

	claim, _ := s.repo.GetBenefitClaimByBrowserKey(r.Context(), webmasterCampaignID, browserKeyHash)
	if claim == nil {
		writeError(w, http.StatusNotFound, "BENEFIT_CLAIM_NOT_FOUND", "未找到领取记录")
		return
	}
	switch claim.Status {
	case "ready":
		sendBenefitReady(w, claim, s.benefit)
	case "revoked", "expired":
		writeError(w, http.StatusGone, "BENEFIT_CLAIM_UNAVAILABLE", "该福利已不可用")
	default:
		w.Header().Set("Retry-After", "3")
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "provisioning", "retryAfterSeconds": 3})
	}
}

// ─── POST /api/admin/benefit-provision (internal admin endpoint) ──────────────

// benefitProvision is an admin-only endpoint to manually trigger provisioning
// for a stuck claim. Not exposed in public routes.
func (s *Server) benefitProvision(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	_ = sess
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "手动 provision 暂不支持")
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func campaignIsActive(campaign *domain.BenefitCampaign) bool {
	if campaign == nil || !campaign.Enabled {
		return false
	}
	now := time.Now().UTC()
	if campaign.StartsAt != "" {
		t, err := time.Parse(time.RFC3339, campaign.StartsAt)
		if err == nil && t.After(now) {
			return false
		}
	}
	if campaign.EndsAt != "" {
		t, err := time.Parse(time.RFC3339, campaign.EndsAt)
		if err == nil && !t.After(now) {
			return false
		}
	}
	return true
}

func sendBenefitReady(w http.ResponseWriter, claim *domain.BenefitClaim, b *BenefitRuntime) {
	if claim.SubscriptionURLEnc == "" || claim.ExpiresAt == "" {
		writeError(w, http.StatusServiceUnavailable, "BENEFIT_RESTORE_UNAVAILABLE", "订阅链接暂时不可用")
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, claim.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusGone, "BENEFIT_CLAIM_UNAVAILABLE", "该福利已过期")
		return
	}
	decrypted, err := b.Cipher.Decrypt(claim.SubscriptionURLEnc, benefit.SubscriptionCipherAAD{
		CampaignID: claim.CampaignID,
		ClaimID:    claim.ID,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "BENEFIT_RESTORE_UNAVAILABLE", "订阅链接暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ready",
		"subscriptionUrl": decrypted,
		"expiresAt":       claim.ExpiresAt,
		"trafficBytes":    webmasterTraffic,
		"durationDays":    webmasterDuration,
		"hwidRequired":    webmasterHWID,
		"ipLimit":         webmasterIPLimit,
	})
}

func newBenefitClaimID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("newBenefitClaimID: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
