package httpapi

import "net/http"

// This valid cost-12 dummy hash is only used to equalize unknown/disabled login
// attempts. No account is associated with it, and comparison never grants access.
const affiliateDummyPasswordHash = "$2a$12$abcdefghijklmnopqrstuu0000000000000000000000000000000"

func writeAffiliateLoginFailure(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "微信号或查询密码不正确")
}

// Do not queue expensive public authentication work: the fixed process-wide
// budget also bounds attempts arriving from many different IPs and account names.
func (s *Server) reserveAffiliateAuth(w http.ResponseWriter) func() {
	select {
	case s.affiliateAuthSlots <- struct{}{}:
		return func() { <-s.affiliateAuthSlots }
	default:
		writeError(w, http.StatusTooManyRequests, "AUTH_BUSY", "查询繁忙，请稍后再试")
		return nil
	}
}
