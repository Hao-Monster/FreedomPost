package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/chatwoot"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/google/uuid"
)

const orderOwnerLifetime = 30 * 24 * time.Hour

type orderMessageClient interface {
	SendOrder(ctx context.Context, token, content, referer string) error
	HandoffCapability(ctx context.Context, token string) (*chatwoot.HandoffCapability, error)
	SendOrderHandoff(ctx context.Context, token, orderID, bindingID, content, referer string) (*chatwoot.HandoffReceipt, error)
}

func (s *Server) orderOwnerCookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-fp_order_owner"
	}
	return "fp_order_owner"
}

func (s *Server) orderOwnerSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" || (r.Header.Get("Origin") != "" && !s.orderOriginAllowed(r)) {
		writeError(w, http.StatusForbidden, "ORIGIN_FORBIDDEN", "请求来源不被允许")
		return
	}
	if s.cfg.CookieSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "ORDER_SESSION_UNAVAILABLE", "下单会话暂时不可用")
		return
	}
	if _, cookie, ok := s.readOrderOwner(r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true, "csrfToken": s.orderCSRF(cookie)})
		return
	}
	id, err := security.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	payload := id + "." + strconv.FormatInt(time.Now().Add(orderOwnerLifetime).Unix(), 10)
	value := payload + "." + security.HMACSign(s.cfg.CookieSecret, "order-owner:v1:"+payload)
	setSessionCookie(w, s.orderOwnerCookieName(), value, int(orderOwnerLifetime.Seconds()), s.cfg.CookieSecure)
	// A first response cannot prove that the browser accepted the cookie.
	writeJSON(w, http.StatusOK, map[string]any{"ready": false})
}

func (s *Server) readOrderOwner(r *http.Request) (string, string, bool) {
	cookie, err := r.Cookie(s.orderOwnerCookieName())
	if err != nil || len(cookie.Value) > 256 || s.cfg.CookieSecret == "" {
		return "", "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || len(parts[0]) != 52 {
		return "", "", false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expires <= time.Now().Unix() || expires > time.Now().Add(orderOwnerLifetime+time.Minute).Unix() {
		return "", "", false
	}
	if !security.HMACEqual(parts[2], security.HMACSign(s.cfg.CookieSecret, "order-owner:v1:"+parts[0]+"."+parts[1])) {
		return "", "", false
	}
	return security.HashText("order-owner:" + parts[0]), cookie.Value, true
}

func (s *Server) orderCSRF(cookie string) string {
	return security.HMACSign(s.cfg.CookieSecret, "order-csrf:v1:"+cookie)
}

func (s *Server) orderOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.Host == r.Host && s.cfg.AllowedOrigins()[origin]
}

func (s *Server) requireOrderOwner(w http.ResponseWriter, r *http.Request, write bool) string {
	w.Header().Set("Cache-Control", "no-store")
	owner, cookie, ok := s.readOrderOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "ORDER_OWNER_REQUIRED", "下单会话已失效，请重新连接")
		return ""
	}
	if write && (!s.orderOriginAllowed(r) || !security.HMACEqual(s.orderCSRF(cookie), r.Header.Get("X-Order-CSRF"))) {
		writeError(w, http.StatusForbidden, "ORDER_CSRF_INVALID", "请刷新页面后重试原订单")
		return ""
	}
	return owner
}

func (s *Server) createOwnedOrder(w http.ResponseWriter, r *http.Request) {
	owner := s.requireOrderOwner(w, r, true)
	if owner == "" {
		return
	}
	requestKey, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil || requestKey == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "下单请求标识无效")
		return
	}
	var input struct {
		ProductSlug         string `json:"productSlug"`
		RecommenderWechatID string `json:"recommenderWechatId"`
		VisitorID           string `json:"visitorId"`
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
	if ok, _ := s.limiter.Allow(r.Context(), "affiliate-order:"+security.HashText(s.remoteIP(r)), 20, time.Hour); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "下单请求过多，请稍后恢复原订单")
		return
	}
	h, created, err := s.repo.CreateOwnedAffiliateOrder(r.Context(), domain.CreateOwnedOrderInput{CreateOrderInput: domain.CreateOrderInput{ProductSlug: input.ProductSlug, AffiliateWechatID: input.RecommenderWechatID}, OwnerHash: owner, RequestKey: requestKey.String()})
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "PRODUCT_UNAVAILABLE", "商品不存在、已售罄或推荐人无效")
		return
	}
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeOrderHandoff(w, status, h)
}

func (s *Server) recoverOwnedOrder(w http.ResponseWriter, r *http.Request) {
	owner := s.requireOrderOwner(w, r, true)
	if owner == "" {
		return
	}
	var input struct {
		RequestKey string `json:"requestKey"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	key, err := uuid.Parse(input.RequestKey)
	if err != nil || key == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "下单请求标识无效")
		return
	}
	h, err := s.repo.RecoverOwnedAffiliateOrder(r.Context(), owner, key.String())
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	writeOrderHandoff(w, http.StatusOK, h)
}

func (s *Server) getOrderSupportDelivery(w http.ResponseWriter, r *http.Request) {
	owner := s.requireOrderOwner(w, r, false)
	if owner == "" {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "未找到此订单")
		return
	}
	h, err := s.repo.GetOwnedOrderHandoff(r.Context(), owner, id.String())
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
}

func (s *Server) sendOrderSupportDelivery(w http.ResponseWriter, r *http.Request) {
	owner := s.requireOrderOwner(w, r, true)
	if owner == "" {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "未找到此订单")
		return
	}
	var input struct {
		ExpectedBindingID string `json:"expectedBindingId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedBindingID == "" || len(input.ExpectedBindingID) > 256 {
		writeError(w, http.StatusBadRequest, "SUPPORT_BINDING_REQUIRED", "请先连接客服会话")
		return
	}
	h, err := s.repo.GetOwnedOrderHandoff(r.Context(), owner, id.String())
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	if h.Delivery.BindingID != "" && h.Delivery.BindingID != input.ExpectedBindingID {
		s.writeOrderHandoffError(w, r, domain.ErrSupportBindingMismatch)
		return
	}
	if h.Delivery.Status == "sent" {
		writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
		return
	}
	if ok, _ := s.limiter.Allow(r.Context(), "order-delivery:"+owner, 30, time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "客服连接请求过多，请稍后重试原订单")
		return
	}
	token := ""
	if cookie, cookieErr := r.Cookie("cw_conversation"); cookieErr == nil {
		token = cookie.Value
	}
	if token == "" || s.chatwoot == nil {
		code := "SUPPORT_SESSION_REQUIRED"
		if s.chatwoot == nil {
			code = "SUPPORT_UNAVAILABLE"
		}
		h, err = s.repo.SetOrderSupportWaiting(r.Context(), owner, id.String(), "awaiting_session", code)
		if s.writeOrderHandoffError(w, r, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
		return
	}
	capability, err := s.chatwoot.HandoffCapability(r.Context(), token)
	if err != nil {
		status, code := handoffFailure(err)
		h, saveErr := s.repo.SetOrderSupportWaiting(r.Context(), owner, id.String(), status, code)
		if s.writeOrderHandoffError(w, r, saveErr) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
		return
	}
	if capability.BindingID != input.ExpectedBindingID {
		s.writeOrderHandoffError(w, r, domain.ErrSupportBindingMismatch)
		return
	}
	h, claimed, err := s.repo.ClaimOrderSupportDelivery(r.Context(), owner, id.String(), capability.BindingID)
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
		return
	}
	content := fmt.Sprintf("你好，我刚刚创建了订单。\n订单号：%s\n商品：%s\n请协助处理，谢谢。", h.Order.OrderCode, h.Order.ProductTitle)
	receipt, sendErr := s.chatwoot.SendOrderHandoff(r.Context(), token, h.Order.ID, capability.BindingID, content, r.Referer())
	status, code, messageID, conversationID := "sent", "", int64(0), int64(0)
	if sendErr != nil {
		status, code = handoffFailure(sendErr)
	} else {
		messageID, conversationID = receipt.MessageID, receipt.ConversationID
	}
	// Persist the known outcome even if the browser aborted its HTTP request.
	// A failure to persist leaves the lease recoverable via the same receiver key.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	h, err = s.repo.FinishOrderSupportDelivery(saveCtx, owner, id.String(), capability.BindingID, h.Delivery.Attempts, status, code, messageID, conversationID)
	if s.writeOrderHandoffError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supportDelivery": h.Delivery})
}

func handoffFailure(err error) (string, string) {
	var failure *chatwoot.HandoffError
	if errors.As(err, &failure) {
		if failure.UnknownOutcome {
			return "reconciling", failure.Code
		}
		if failure.Retryable {
			return "retry_wait", failure.Code
		}
		return "action_required", failure.Code
	}
	return "action_required", "SUPPORT_REQUEST_FAILED"
}

func writeOrderHandoff(w http.ResponseWriter, status int, h *domain.OrderHandoff) {
	legacyStatus := "pending"
	if h.Delivery.Status == "sent" {
		legacyStatus = "sent"
	}
	writeJSON(w, status, map[string]any{"order": h.Order, "supportMessageStatus": legacyStatus, "supportDelivery": h.Delivery})
}

func (s *Server) writeOrderHandoffError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "未找到此订单或商品暂不可用")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "此下单请求已用于另一商品或推荐人")
	case errors.Is(err, domain.ErrSupportBindingMismatch):
		writeError(w, http.StatusConflict, "SUPPORT_BINDING_MISMATCH", "客服身份发生变化，请恢复原会话后重试")
	case errors.Is(err, domain.ErrInvalidRecommender):
		writeError(w, http.StatusBadRequest, "INVALID_RECOMMENDER", "推荐人微信号不存在或已停用")
	case errors.Is(err, domain.ErrInvalidState):
		writeError(w, http.StatusConflict, "INVALID_PRICING", "订单状态或商品定价无效")
	default:
		s.internalError(w, r, err)
	}
	return true
}
