package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/auth"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/turnstile"
)

const (
	sessionCookieName = "fp_reader_session"
	sessionMaxAge     = 30 * 24 * 60 * 60
)

type TurnstileVerifier interface {
	Verify(context.Context, string, string, string) error
}

type Limiter interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
}

type Config struct {
	Enabled            bool
	Store              domain.Store
	Turnstile          TurnstileVerifier
	Limiter            Limiter
	TurnstileSiteKey   string
	CookieSecure       bool
	PublicOrigin       string
	TrustProxy         bool
	InternalSecret     string
	SupportWechatImage string
	Logger             *slog.Logger
}

type API struct {
	config    Config
	mux       *http.ServeMux
	origin    string
	dummyHash string
	logger    *slog.Logger
}

func New(config Config) (http.Handler, error) {
	if config.Store == nil || config.Turnstile == nil || config.Limiter == nil {
		return nil, errors.New("store, Turnstile verifier and limiter are required")
	}
	if len(config.InternalSecret) < 32 {
		return nil, errors.New("PAID_ACCESS_INTERNAL_SECRET must contain at least 32 characters")
	}
	origin, err := normalizeOrigin(config.PublicOrigin)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{config: config, mux: http.NewServeMux(), origin: origin, dummyHash: auth.DummyHash(), logger: logger}
	api.routes()
	return api.securityHeaders(api.mux), nil
}

func (api *API) routes() {
	api.mux.HandleFunc("GET /healthz", api.health)
	api.mux.HandleFunc("GET /api/reader/config", api.publicConfig)
	api.mux.HandleFunc("POST /api/reader/register", api.register)
	api.mux.HandleFunc("POST /api/reader/login", api.login)
	api.mux.HandleFunc("POST /api/reader/logout", api.logout)
	api.mux.HandleFunc("GET /api/reader/session", api.session)
	api.mux.HandleFunc("GET /api/reader/orders", api.accountOrders)
	api.mux.HandleFunc("GET /api/reader/posts/{slug}", api.article)
	api.mux.HandleFunc("POST /api/reader/posts/{slug}/orders", api.createOrder)
	api.mux.HandleFunc("POST /internal/access/check", api.internalAccessCheck)
	api.mux.HandleFunc("GET /internal/article-orders", api.internalOrders)
	api.mux.HandleFunc("PATCH /internal/article-orders/{id}", api.internalUpdateOrder)
	api.mux.HandleFunc("GET /internal/reader-accounts", api.internalAccounts)
	api.mux.HandleFunc("POST /internal/reader-accounts/{id}/reset-password", api.internalResetPassword)
}

func (api *API) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "enabled": api.config.Enabled})
}

func (api *API) publicConfig(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"enabled":          api.config.Enabled,
		"turnstileSiteKey": api.config.TurnstileSiteKey,
		"actions":          map[string]string{"register": "reader_register", "login": "reader_login"},
	})
}

type credentialRequest struct {
	LoginName      string `json:"loginName"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstileToken"`
}

func (api *API) register(response http.ResponseWriter, request *http.Request) {
	if !api.requireEnabled(response) || !api.validOrigin(request) {
		if api.config.Enabled {
			writeError(response, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
		}
		return
	}
	if !api.allow(request, "register", 8, time.Hour) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "注册请求过多，请稍后再试")
		return
	}
	var input credentialRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	login, err := auth.NormalizeLogin(input.LoginName)
	if err != nil || auth.ValidatePassword(input.Password) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_CREDENTIALS", "登录名或密码格式不正确")
		return
	}
	if !api.allowKey(request, "register:login:"+hashText(login.Normalized), 4, time.Hour) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "该登录名的注册请求过多，请稍后再试")
		return
	}
	if err := api.config.Turnstile.Verify(request.Context(), input.TurnstileToken, api.remoteIP(request), "reader_register"); err != nil {
		api.writeTurnstileError(response, err)
		return
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	account, err := api.config.Store.CreateAccount(request.Context(), login.Display, login.Normalized, passwordHash)
	if errors.Is(err, domain.ErrConflict) {
		writeError(response, http.StatusConflict, "LOGIN_NAME_TAKEN", "该登录名已被使用")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if err := api.issueSession(response, request, account); err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"account": publicAccount(account)})
}

func (api *API) login(response http.ResponseWriter, request *http.Request) {
	if !api.requireEnabled(response) || !api.validOrigin(request) {
		if api.config.Enabled {
			writeError(response, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
		}
		return
	}
	if !api.allow(request, "login", 12, 15*time.Minute) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "登录尝试过多，请稍后再试")
		return
	}
	var input credentialRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	login, normalizeErr := auth.NormalizeLogin(input.LoginName)
	if normalizeErr != nil || len(input.Password) > 1024 {
		writeError(response, http.StatusUnauthorized, "LOGIN_FAILED", "登录名或密码不正确")
		return
	}
	if !api.allowKey(request, "login:name:"+hashText(login.Normalized), 8, 15*time.Minute) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "登录尝试过多，请稍后再试")
		return
	}
	if err := api.config.Turnstile.Verify(request.Context(), input.TurnstileToken, api.remoteIP(request), "reader_login"); err != nil {
		api.writeTurnstileError(response, err)
		return
	}
	account, err := api.config.Store.FindAccountByLogin(request.Context(), login.Normalized)
	passwordHash := api.dummyHash
	if err == nil {
		passwordHash = account.PasswordHash
	}
	valid := auth.VerifyPassword(passwordHash, input.Password)
	if err != nil || !valid || account.Status != "active" {
		writeError(response, http.StatusUnauthorized, "LOGIN_FAILED", "登录名或密码不正确")
		return
	}
	if err := api.issueSession(response, request, account); err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"account": publicAccount(account)})
}

func (api *API) logout(response http.ResponseWriter, request *http.Request) {
	if !api.validOrigin(request) {
		writeError(response, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
		return
	}
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		_ = api.config.Store.RevokeSession(request.Context(), auth.HashSessionToken(cookie.Value))
	}
	http.SetCookie(response, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: api.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (api *API) session(response http.ResponseWriter, request *http.Request) {
	account, _, err := api.authenticate(response, request)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"account": publicAccount(account)})
}

func (api *API) article(response http.ResponseWriter, request *http.Request) {
	if !api.requireEnabled(response) {
		return
	}
	if !api.allow(request, "article", 240, time.Minute) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "文章请求过于频繁")
		return
	}
	article, err := api.config.Store.FindArticle(request.Context(), request.PathValue("slug"))
	if errors.Is(err, domain.ErrNotFound) || article.Visibility == "private" {
		writeError(response, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	access := map[string]bool{"locked": false, "authenticated": false, "purchased": false}
	if article.Visibility == "paid" {
		account, _, authErr := api.authenticate(response, request)
		if authErr == nil {
			access["authenticated"] = true
			entitled, entitlementErr := api.config.Store.HasEntitlement(request.Context(), account.ID, article.ID)
			if entitlementErr != nil {
				api.internalError(response, request, entitlementErr)
				return
			}
			access["purchased"] = entitled
		} else if !errors.Is(authErr, domain.ErrNotFound) {
			api.internalError(response, request, authErr)
			return
		}
		access["locked"] = !access["purchased"]
		if access["locked"] {
			article.Excerpt = ""
			article.ContentHTML = ""
			article.AttachmentCount = 0
		}
	}
	response.Header().Set("Cache-Control", "private, no-store")
	writeJSON(response, http.StatusOK, map[string]any{
		"item":    article,
		"access":  access,
		"contact": map[string]string{"wechatImageUrl": api.config.SupportWechatImage},
	})
}

func (api *API) createOrder(response http.ResponseWriter, request *http.Request) {
	if !api.requireEnabled(response) || !api.validOrigin(request) {
		if api.config.Enabled {
			writeError(response, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
		}
		return
	}
	account, _, err := api.authenticate(response, request)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if !api.allowAccount(request, account.ID, "order", 20, time.Hour) {
		writeError(response, http.StatusTooManyRequests, "RATE_LIMITED", "下单次数过多，请稍后再试")
		return
	}
	article, err := api.config.Store.FindArticle(request.Context(), request.PathValue("slug"))
	if errors.Is(err, domain.ErrNotFound) || article.Visibility != "paid" {
		writeError(response, http.StatusNotFound, "POST_NOT_FOUND", "付费文章不存在")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	order, created, err := api.config.Store.CreateOrder(request.Context(), account.ID, article)
	if errors.Is(err, domain.ErrAlreadyEntitled) {
		writeError(response, http.StatusConflict, "ALREADY_PURCHASED", "已拥有该文章阅读权限")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, map[string]any{"order": order, "created": created, "contact": map[string]string{"wechatImageUrl": api.config.SupportWechatImage}})
}

func (api *API) accountOrders(response http.ResponseWriter, request *http.Request) {
	account, _, err := api.authenticate(response, request)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	orders, err := api.config.Store.ListAccountOrders(request.Context(), account.ID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": orders})
}

func (api *API) internalAccessCheck(response http.ResponseWriter, request *http.Request) {
	body, ok := readInternalBody(response, request)
	if !ok || !api.validInternalRequest(request, body) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未授权")
		return
	}
	var input struct {
		SessionToken string `json:"sessionToken"`
		PostSlug     string `json:"postSlug"`
	}
	if json.Unmarshal(body, &input) != nil || input.SessionToken == "" || input.PostSlug == "" {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "请求无效")
		return
	}
	account, err := api.config.Store.FindAccountBySession(request.Context(), auth.HashSessionToken(input.SessionToken))
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(response, http.StatusOK, map[string]bool{"allowed": false})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	article, err := api.config.Store.FindArticle(request.Context(), input.PostSlug)
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(response, http.StatusOK, map[string]bool{"allowed": false})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	allowed := article.Visibility == "public"
	if article.Visibility == "paid" {
		allowed, err = api.config.Store.HasEntitlement(request.Context(), account.ID, article.ID)
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"allowed": allowed})
}

func (api *API) internalOrders(response http.ResponseWriter, request *http.Request) {
	if !api.validInternalAdminRequest(request, nil) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未授权")
		return
	}
	orders, err := api.config.Store.ListAdminOrders(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": orders})
}

func (api *API) internalUpdateOrder(response http.ResponseWriter, request *http.Request) {
	body, ok := readInternalBody(response, request)
	if !ok || !api.validInternalAdminRequest(request, body) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未授权")
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &input) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "请求无效")
		return
	}
	order, err := api.config.Store.UpdateOrderStatus(request.Context(), request.PathValue("id"), input.Status, internalActor(request))
	if errors.Is(err, domain.ErrInvalidState) {
		writeError(response, http.StatusConflict, "INVALID_ORDER_STATE", "订单状态不能这样变更")
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		writeError(response, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"order": order})
}

func (api *API) internalAccounts(response http.ResponseWriter, request *http.Request) {
	if !api.validInternalAdminRequest(request, nil) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未授权")
		return
	}
	accounts, err := api.config.Store.ListAccounts(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, publicAccount(account))
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (api *API) internalResetPassword(response http.ResponseWriter, request *http.Request) {
	body, ok := readInternalBody(response, request)
	if !ok || !api.validInternalAdminRequest(request, body) {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "未授权")
		return
	}
	password, err := auth.RandomPassword()
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if err := api.config.Store.ResetPassword(request.Context(), request.PathValue("id"), hash, internalActor(request)); errors.Is(err, domain.ErrNotFound) {
		writeError(response, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "账号不存在")
		return
	} else if err != nil {
		api.internalError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"temporaryPassword": password})
}

func (api *API) authenticate(response http.ResponseWriter, request *http.Request) (domain.Account, string, error) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) < 40 || len(cookie.Value) > 128 {
		return domain.Account{}, "", domain.ErrNotFound
	}
	hash := auth.HashSessionToken(cookie.Value)
	account, err := api.config.Store.FindAccountBySession(request.Context(), hash)
	if err != nil {
		return domain.Account{}, "", err
	}
	_ = api.config.Store.TouchSession(request.Context(), hash)
	http.SetCookie(response, &http.Cookie{Name: sessionCookieName, Value: cookie.Value, Path: "/", MaxAge: sessionMaxAge, HttpOnly: true, Secure: api.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	return account, hash, nil
}

func (api *API) issueSession(response http.ResponseWriter, request *http.Request, account domain.Account) error {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return err
	}
	metadata := domain.SessionMetadata{UserAgentHash: hashText(request.UserAgent()), IPHash: hashText(api.remoteIP(request))}
	if err := api.config.Store.CreateSession(request.Context(), account.ID, hash, account.CredentialVersion, metadata); err != nil {
		return err
	}
	http.SetCookie(response, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", MaxAge: sessionMaxAge, HttpOnly: true, Secure: api.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	return nil
}

func (api *API) requireEnabled(response http.ResponseWriter) bool {
	if api.config.Enabled {
		return true
	}
	writeError(response, http.StatusNotFound, "FEATURE_DISABLED", "功能暂未开放")
	return false
}

func (api *API) validOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin != "" && origin == api.origin
}

func (api *API) allow(request *http.Request, action string, limit int, window time.Duration) bool {
	allowed, err := api.config.Limiter.Allow(request.Context(), action+":ip:"+hashText(api.remoteIP(request)), limit, window)
	if err != nil {
		api.logger.Warn("Redis rate limiter unavailable; memory fallback used", "action", action)
	}
	return allowed
}

func (api *API) allowAccount(request *http.Request, accountID, action string, limit int, window time.Duration) bool {
	allowed, err := api.config.Limiter.Allow(request.Context(), action+":account:"+accountID, limit, window)
	if err != nil {
		api.logger.Warn("Redis rate limiter unavailable; memory fallback used", "action", action)
	}
	return allowed
}

func (api *API) allowKey(request *http.Request, key string, limit int, window time.Duration) bool {
	allowed, err := api.config.Limiter.Allow(request.Context(), key, limit, window)
	if err != nil {
		api.logger.Warn("Redis rate limiter unavailable; memory fallback used", "keyScope", strings.Split(key, ":")[0])
	}
	return allowed
}

func (api *API) remoteIP(request *http.Request) string {
	if api.config.TrustProxy {
		if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func (api *API) writeTurnstileError(response http.ResponseWriter, err error) {
	if errors.Is(err, turnstile.ErrUnavailable) {
		writeError(response, http.StatusServiceUnavailable, "TURNSTILE_UNAVAILABLE", "人机验证服务暂不可用")
		return
	}
	writeError(response, http.StatusBadRequest, "TURNSTILE_REJECTED", "人机验证未通过")
}

func (api *API) internalError(response http.ResponseWriter, request *http.Request, err error) {
	api.logger.Error("paid access request failed", "method", request.Method, "path", request.URL.Path, "error", err)
	writeError(response, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时不可用")
}

func (api *API) validInternalRequest(request *http.Request, body []byte) bool {
	timestampText := request.Header.Get("X-FreedomPost-Timestamp")
	nonce := request.Header.Get("X-FreedomPost-Nonce")
	actor := request.Header.Get("X-FreedomPost-Admin")
	signatureText := request.Header.Get("X-FreedomPost-Signature")
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || abs(time.Now().Unix()-timestamp) > 300 || len(nonce) < 16 || len(nonce) > 128 || !validInternalActor(actor) {
		return false
	}
	bodyHash := sha256.Sum256(body)
	canonical := timestampText + "\n" + nonce + "\n" + request.Method + "\n" + request.URL.Path + "\n" + actor + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(api.config.InternalSecret))
	_, _ = mac.Write([]byte(canonical))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signatureText)) {
		return false
	}
	allowed, _ := api.config.Limiter.Allow(request.Context(), "internal-nonce:"+hashText(nonce), 1, 10*time.Minute)
	return allowed
}

func (api *API) validInternalAdminRequest(request *http.Request, body []byte) bool {
	return request.Header.Get("X-FreedomPost-Admin") != "" && api.validInternalRequest(request, body)
}

func validInternalActor(actor string) bool {
	if len(actor) > 128 || !utf8.ValidString(actor) {
		return false
	}
	for _, character := range actor {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (api *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(response, request)
	})
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(response, http.StatusBadRequest, "INVALID_JSON", "请求格式不正确")
		return false
	}
	return true
}

func readInternalBody(response http.ResponseWriter, request *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 16*1024+1))
	if err != nil || len(body) > 16*1024 {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "请求无效")
		return nil, false
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

func normalizeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("PUBLIC_SITE_URL must be an origin without path, query or fragment")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func publicAccount(account domain.Account) map[string]any {
	return map[string]any{"id": account.ID, "loginName": account.LoginName, "status": account.Status, "createdAt": account.CreatedAt}
}
func internalActor(request *http.Request) string {
	return request.Header.Get("X-FreedomPost-Admin")
}
func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("encode HTTP response", "error", err)
	}
}
