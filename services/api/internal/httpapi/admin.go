package httpapi

import (
	"net/http"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

// ─── Admin authentication ─────────────────────────────────────────────────────

// requireAdmin authenticates the admin session from cookie.
// Returns the session or writes a 401 and returns nil.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *domain.AdminSession {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	tokenHash := security.HashToken(cookie.Value)
	session, err := s.sessions.GetAdminSession(r.Context(), tokenHash)
	if err != nil {
		s.logger.Error("get admin session", "error", err)
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	if session == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	return session
}

// requireAffiliate authenticates the affiliate session from cookie.
func (s *Server) requireAffiliate(w http.ResponseWriter, r *http.Request) *domain.AffiliateSession {
	cookie, err := r.Cookie(affiliateCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	tokenHash := security.HashToken(cookie.Value)
	session, err := s.sessions.GetAffiliateSession(r.Context(), tokenHash)
	if err != nil {
		s.logger.Error("get affiliate session", "error", err)
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	if session == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录")
		return nil
	}
	return session
}

// ─── Admin handlers ───────────────────────────────────────────────────────────

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	// Rate limit: 5 attempts per 15 minutes per IP (missing from TS version!)
	ip := s.remoteIP(r)
	allowed, err := s.limiter.Allow(r.Context(), "admin-login:"+security.HashText(ip), 5, 15*time.Minute)
	if err != nil {
		s.logger.Warn("rate limit check failed", "error", err)
	}
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "登录尝试过多，请稍后再试")
		return
	}

	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	// Verify credentials using constant-time comparison
	if !security.HMACEqual(input.Username, s.cfg.AdminUsername) {
		writeError(w, http.StatusUnauthorized, "LOGIN_FAILED", "用户名或密码不正确")
		return
	}
	// Compare password: auto-detect bcrypt hash vs plaintext dev password
	if !verifyAdminPassword(s.cfg.AdminPasswordHash, input.Password) {
		writeError(w, http.StatusUnauthorized, "LOGIN_FAILED", "用户名或密码不正确")
		return
	}

	token, err := security.NewToken()

	if err != nil {
		s.internalError(w, r, err)
		return
	}

	sess := domain.AdminSession{
		Username:  s.cfg.AdminUsername,
		CreatedAt: time.Now(),
	}
	if err := s.sessions.SetAdminSession(r.Context(), security.HashToken(token), sess); err != nil {
		s.internalError(w, r, err)
		return
	}

	setSessionCookie(w, adminCookieName, token, int((24 * time.Hour).Seconds()), s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminCookieName); err == nil {
		_ = s.sessions.DeleteAdminSession(r.Context(), security.HashToken(cookie.Value))
	}
	clearCookie(w, adminCookieName, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminGetSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

// ─── Post admin handlers ──────────────────────────────────────────────────────

func (s *Server) adminListPosts(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	posts, err := s.repo.ListPosts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if posts == nil {
		posts = []domain.Post{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": posts, "posts": posts})
}

func (s *Server) adminCreatePost(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input struct {
		Title      string `json:"title"`
		Visibility string `json:"visibility"`
		PriceCents int    `json:"priceCents"`
		Currency   string `json:"currency"`
		Content    string `json:"content"`
		Markdown   string `json:"markdown"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	content := input.Content
	if content == "" {
		content = input.Markdown
	}

	rendered := renderMarkdown(content)
	slug, err := security.GenerateSlug()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = slug // slug is set by repository using unique slug generation

	post, err := s.repo.CreatePost(r.Context(), domain.CreatePostInput{
		Title:           sanitizeTitle(input.Title),
		Visibility:      domain.PostVisibility(input.Visibility),
		PriceCents:      input.PriceCents,
		Currency:        coalesce(input.Currency, "CNY"),
		ContentMarkdown: content,
		ContentHTML:     rendered.HTML,
		SearchText:      rendered.SearchText,
		Excerpt:         rendered.Excerpt,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.searchCache.Invalidate()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              post.ID,
		"slug":            post.Slug,
		"title":           post.Title,
		"markdown":        post.ContentMarkdown,
		"contentMarkdown": post.ContentMarkdown,
		"contentHtml":     post.ContentHTML,
		"visibility":      post.Visibility,
		"priceCents":      post.PriceCents,
		"currency":        post.Currency,
		"viewCount":       post.ViewCount,
		"commentCount":    post.CommentCount,
		"attachmentCount": post.AttachmentCount,
		"createdAt":       post.CreatedAt,
		"updatedAt":       post.UpdatedAt,
		"post":            post,
	})
}

func (s *Server) adminUpdatePost(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id := r.PathValue("id")
	var input struct {
		Title      string `json:"title"`
		Slug       string `json:"slug"`
		Visibility string `json:"visibility"`
		PriceCents int    `json:"priceCents"`
		Currency   string `json:"currency"`
		Content    string `json:"content"`
		Markdown   string `json:"markdown"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	content := input.Content
	if content == "" {
		content = input.Markdown
	}

	rendered := renderMarkdown(content)
	post, err := s.repo.UpdatePost(r.Context(), domain.UpdatePostInput{
		ID:              id,
		Title:           sanitizeTitle(input.Title),
		Slug:            input.Slug,
		Visibility:      domain.PostVisibility(input.Visibility),
		PriceCents:      input.PriceCents,
		Currency:        coalesce(input.Currency, "CNY"),
		ContentMarkdown: content,
		ContentHTML:     rendered.HTML,
		SearchText:      rendered.SearchText,
		Excerpt:         rendered.Excerpt,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if post == nil {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	s.searchCache.Invalidate()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              post.ID,
		"slug":            post.Slug,
		"title":           post.Title,
		"markdown":        post.ContentMarkdown,
		"contentMarkdown": post.ContentMarkdown,
		"contentHtml":     post.ContentHTML,
		"visibility":      post.Visibility,
		"priceCents":      post.PriceCents,
		"currency":        post.Currency,
		"viewCount":       post.ViewCount,
		"commentCount":    post.CommentCount,
		"attachmentCount": post.AttachmentCount,
		"createdAt":       post.CreatedAt,
		"updatedAt":       post.UpdatedAt,
		"post":            post,
	})
}

func (s *Server) adminDeletePost(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id := r.PathValue("id")
	deleted, err := s.repo.DeletePost(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	s.searchCache.Invalidate()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminUploadAttachment(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.handleUpload(w, r, "post", "", "admin")
}

// ─── Product admin handlers ───────────────────────────────────────────────────

func (s *Server) adminListProducts(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	products, err := s.repo.ListProducts(r.Context(), false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if products == nil {
		products = []domain.Product{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": products, "products": products})
}

func (s *Server) adminCreateProduct(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input domain.ProductInput
	if !decodeJSON(w, r, &input) {
		return
	}
	product, err := s.repo.CreateProduct(r.Context(), input)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"product": product})
}

func (s *Server) adminUpdateProduct(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input domain.ProductInput
	if !decodeJSON(w, r, &input) {
		return
	}
	product, err := s.repo.UpdateProduct(r.Context(), r.PathValue("id"), input)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if product == nil {
		writeError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"product": product})
}

func (s *Server) adminDeleteProduct(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	deleted, err := s.repo.DeleteProduct(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── Tool admin handlers ──────────────────────────────────────────────────────

func (s *Server) adminListTools(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	tools, err := s.repo.ListTools(r.Context(), false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tools == nil {
		tools = []domain.Tool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tools, "tools": tools})
}

func (s *Server) adminCreateTool(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input domain.ToolInput
	if !decodeJSON(w, r, &input) {
		return
	}
	tool, err := s.repo.CreateTool(r.Context(), input)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tool": tool})
}

func (s *Server) adminUpdateTool(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input domain.ToolInput
	if !decodeJSON(w, r, &input) {
		return
	}
	tool, err := s.repo.UpdateTool(r.Context(), r.PathValue("id"), input)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tool == nil {
		writeError(w, http.StatusNotFound, "TOOL_NOT_FOUND", "工具不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool": tool})
}

func (s *Server) adminDeleteTool(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	deleted, err := s.repo.DeleteTool(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "TOOL_NOT_FOUND", "工具不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── Affiliate admin handlers ─────────────────────────────────────────────────

func (s *Server) adminListAffiliates(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	affiliates, err := s.repo.ListAffiliates(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if affiliates == nil {
		affiliates = []domain.AffiliateListItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": affiliates, "affiliates": affiliates})
}

func (s *Server) adminUpdateAffiliate(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	updated, err := s.repo.UpdateAffiliateStatus(r.Context(), r.PathValue("id"), domain.AffiliateStatus(input.Status))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "AFFILIATE_NOT_FOUND", "推广者不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminResetAffiliatePassword(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	hash, err := bcryptHashReal(input.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	updated, err := s.repo.UpdateAffiliatePassword(r.Context(), r.PathValue("id"), hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "AFFILIATE_NOT_FOUND", "推广者不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminListAffiliateOrders(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	orders, err := s.repo.ListAffiliateOrders(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if orders == nil {
		orders = []domain.AffiliateOrder{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orders, "orders": orders})
}

func (s *Server) adminUpdateAffiliateOrder(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input struct {
		Status string `json:"status"`
		Notes  string `json:"notes"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	updated, err := s.repo.UpdateAffiliateOrderStatus(r.Context(), r.PathValue("id"), input.Status, input.Notes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── Reader account & article order admin handlers (via paid-access) ──────────

func (s *Server) adminListArticleOrders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	data, err := s.paidAccess.ListArticleOrders(r.Context(), sess.Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) adminUpdateArticleOrder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// Re-serialize for paid-access
	data, err := s.paidAccess.UpdateArticleOrderStatus(r.Context(), r.PathValue("id"), input.Status, sess.Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) adminListReaderAccounts(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	data, err := s.paidAccess.ListReaderAccounts(r.Context(), sess.Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) adminResetReaderPassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdmin(w, r)
	if sess == nil {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	hash, err := bcryptHashReal(input.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data, err := s.paidAccess.ResetReaderPassword(r.Context(), r.PathValue("id"), hash, sess.Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ─── Input helpers ────────────────────────────────────────────────────────────

func sanitizeTitle(title string) string {
	title = trimStr(title)
	if title == "" {
		return "未命名文章"
	}
	return title
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
