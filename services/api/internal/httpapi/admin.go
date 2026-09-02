package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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
		s.logger.Error("get admin session", "request_id", requestIDFromRequest(r), "error", err)
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
	affiliate, err := s.repo.GetAffiliateByID(r.Context(), session.AffiliateID)
	if err != nil {
		s.internalError(w, r, err)
		return nil
	}
	if affiliate == nil || affiliate.Status != domain.AffiliateStatusActive || affiliate.CredentialVersion != session.CredentialVersion {
		_ = s.sessions.DeleteAffiliateSession(r.Context(), tokenHash)
		clearCookie(w, affiliateCookieName, s.cfg.CookieSecure)
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "登录已失效")
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
	if hosts := s.unmanagedArticleImageHosts(rendered.HTML); len(hosts) > 0 {
		writeImageImportError(w, http.StatusBadRequest, "EXTERNAL_IMAGE_NOT_IMPORTED", "文章包含尚未转存的外链图片", "请重新粘贴图片并等待转存完成，或上传本地图片后再保存")
		return
	}
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
		Title          string                    `json:"title"`
		Slug           string                    `json:"slug"`
		Visibility     string                    `json:"visibility"`
		PriceCents     int                       `json:"priceCents"`
		Currency       string                    `json:"currency"`
		Content        string                    `json:"content"`
		Markdown       string                    `json:"markdown"`
		ImportedImages []domain.PendingPostImage `json:"importedImages"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	content := input.Content
	if content == "" {
		content = input.Markdown
	}

	rendered := renderMarkdown(content)
	if hosts := s.unmanagedArticleImageHosts(rendered.HTML); len(hosts) > 0 {
		writeImageImportError(w, http.StatusBadRequest, "EXTERNAL_IMAGE_NOT_IMPORTED", "文章包含尚未转存的外链图片", "请重新粘贴图片并等待转存完成，或上传本地图片后再保存")
		return
	}
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
		ImportedImages:  input.ImportedImages,
	})
	if errors.Is(err, domain.ErrInvalidAttachment) {
		writeImageImportError(w, http.StatusBadRequest, "INVALID_IMPORTED_IMAGE", "已转存图片无效、已过期或未插入文章", "请重新粘贴该图片，或上传本地图片后再保存")
		return
	}
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
	s.handleUpload(w, r, "post", "", "admin", s.cfg.UploadMaxBytes)
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
	input, issues := normalizeProductInputWithIssues(input)
	if len(issues) > 0 {
		writeProductValidationError(w, issues)
		return
	}
	product, err := s.repo.CreateProduct(r.Context(), input)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, flattenedResponse("product", product))
}

func (s *Server) adminUpdateProduct(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var input domain.ProductInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input, issues := normalizeProductInputWithIssues(input)
	if len(issues) > 0 {
		writeProductValidationError(w, issues)
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
	writeJSON(w, http.StatusOK, flattenedResponse("product", product))
}

func normalizeProductInput(input domain.ProductInput) (domain.ProductInput, bool) {
	normalized, issues := normalizeProductInputWithIssues(input)
	if len(issues) > 0 {
		return domain.ProductInput{}, false
	}
	return normalized, true
}

type productInputIssue struct {
	Field      string `json:"field"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Resolution string `json:"resolution"`
}

func normalizeProductInputWithIssues(input domain.ProductInput) (domain.ProductInput, []productInputIssue) {
	input.Title = strings.TrimSpace(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Description = strings.TrimSpace(input.Description)
	input.Category = strings.TrimSpace(input.Category)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.CoverURL = strings.TrimSpace(input.CoverURL)
	input.LinkURL = strings.TrimSpace(input.LinkURL)
	input.Status = strings.TrimSpace(input.Status)
	if input.Category == "" {
		input.Category = "other"
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}

	issues := make([]productInputIssue, 0)
	addIssue := func(field, code, message, resolution string) {
		issues = append(issues, productInputIssue{Field: field, Code: code, Message: message, Resolution: resolution})
	}

	for _, field := range input.MissingRequiredJSONFields() {
		switch field {
		case "priceCents":
			addIssue(field, "REQUIRED", "价格不能为空", "请填写商品价格")
		case "compareAtCents":
			addIssue(field, "REQUIRED", "划线价字段缺失", "不设置划线价时请明确留空")
		case "stock":
			addIssue(field, "REQUIRED", "库存不能为空", "请填写库存，-1 表示不限量")
		case "sortOrder":
			addIssue(field, "REQUIRED", "排序不能为空", "请填写 -100000 到 100000 之间的整数")
		}
	}

	if input.Title == "" {
		addIssue("title", "REQUIRED", "商品名称不能为空", "请输入商品名称（最多 120 个字符）")
	} else if utf8.RuneCountInString(input.Title) > 120 {
		addIssue("title", "TOO_LONG", "商品名称不能超过 120 个字符", "请缩短商品名称后重试")
	}
	if input.Summary == "" {
		addIssue("summary", "REQUIRED", "一句话简介不能为空", "请输入一句话简介（最多 500 个字符）")
	} else if utf8.RuneCountInString(input.Summary) > 500 {
		addIssue("summary", "TOO_LONG", "一句话简介不能超过 500 个字符", "请缩短简介后重试")
	}
	if input.Description == "" {
		addIssue("description", "REQUIRED", "商品详情不能为空", "请输入商品详情（最多 12000 个字符）")
	} else if utf8.RuneCountInString(input.Description) > 12000 {
		addIssue("description", "TOO_LONG", "商品详情不能超过 12000 个字符", "请缩短商品详情后重试")
	}
	if utf8.RuneCountInString(input.Category) > 32 {
		addIssue("category", "TOO_LONG", "商品分类不能超过 32 个字符", "请选择有效的商品分类")
	}
	if utf8.RuneCountInString(input.Currency) > 8 {
		addIssue("currency", "TOO_LONG", "币种代码不能超过 8 个字符", "请选择支持的币种")
	}
	if input.Status != "draft" && input.Status != "published" {
		addIssue("status", "INVALID_VALUE", "发布状态无效", "请选择草稿或公开发布")
	}
	if input.PriceCents < 0 || input.PriceCents > 100_000_000 {
		addIssue("priceCents", "OUT_OF_RANGE", "价格必须在 0 到 1000000.00 之间", "请修改商品价格后重试")
	}
	if input.CommissionCents < 0 || input.CommissionCents > 100_000_000 {
		addIssue("commissionCents", "OUT_OF_RANGE", "分销佣金必须在 0 到 1000000.00 之间", "请修改分销佣金后重试")
	}
	if input.Stock < -1 || input.Stock > 1_000_000 {
		addIssue("stock", "OUT_OF_RANGE", "库存必须是 -1 到 1000000 之间的整数", "请修改库存，-1 表示不限量")
	}
	if input.SoldCount < 0 || input.SoldCount > 1_000_000 {
		addIssue("soldCount", "OUT_OF_RANGE", "已售出数量必须是 0 到 1000000 之间的整数", "请修改已售出数量后重试")
	}
	if input.SortOrder < -100_000 || input.SortOrder > 100_000 {
		addIssue("sortOrder", "OUT_OF_RANGE", "排序必须是 -100000 到 100000 之间的整数", "请修改排序后重试")
	}
	if !validProductURL(input.CoverURL) {
		addIssue("coverUrl", "INVALID_URL", "商品封面地址无效", "请重新上传封面，或清空无效地址")
	}
	if !validProductURL(input.LinkURL) {
		addIssue("linkUrl", "INVALID_URL", "商品链接地址无效", "请使用站内路径或 http/https 地址")
	}
	if input.CompareAtCents != nil {
		if *input.CompareAtCents > 100_000_000 {
			addIssue("compareAtCents", "OUT_OF_RANGE", "划线价不能超过 1000000.00", "请降低划线价，或留空")
		} else if *input.CompareAtCents < input.PriceCents {
			minimum := fmt.Sprintf("%.2f %s", float64(input.PriceCents)/100, input.Currency)
			addIssue("compareAtCents", "COMPARE_AT_BELOW_PRICE", "划线价不能低于售价", fmt.Sprintf("请将划线价设为不低于 %s，或留空", minimum))
		}
	}
	return input, issues
}

func writeProductValidationError(w http.ResponseWriter, issues []productInputIssue) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    "INVALID_PRODUCT",
			"message": fmt.Sprintf("请修正 %d 项商品信息", len(issues)),
			"issues":  issues,
		},
	})
}

func validProductURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2000 || strings.HasPrefix(value, "//") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" {
		return parsed.Host == ""
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
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
	writeJSON(w, http.StatusCreated, flattenedResponse("tool", tool))
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
	writeJSON(w, http.StatusOK, flattenedResponse("tool", tool))
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
	status := domain.AffiliateStatus(input.Status)
	if status != domain.AffiliateStatusActive && status != domain.AffiliateStatusDisabled {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "推广者状态无效")
		return
	}
	updated, err := s.repo.UpdateAffiliateStatus(r.Context(), r.PathValue("id"), status)
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
	if !validAffiliatePassword(input.Password) {
		writeError(w, http.StatusBadRequest, "INVALID_PASSWORD", "密码长度必须为 8 到 72 字节")
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

func validAffiliatePassword(password string) bool {
	password = strings.TrimSpace(password)
	return len(password) >= 8 && len(password) <= 72
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
		OrderStatus      string `json:"orderStatus"`
		CommissionStatus string `json:"commissionStatus"`
		Status           string `json:"status"` // legacy alias
		Notes            string `json:"notes"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.OrderStatus == "" {
		input.OrderStatus = input.Status
	}
	if input.CommissionStatus == "" {
		if input.OrderStatus == "completed" {
			input.CommissionStatus = "pending"
		} else {
			input.CommissionStatus = "not_due"
		}
	}
	if !validAffiliateOrderStatus(input.OrderStatus) || !validAffiliateCommissionStatus(input.CommissionStatus) || (input.CommissionStatus == "paid" && input.OrderStatus != "completed") {
		writeError(w, http.StatusBadRequest, "INVALID_ORDER_STATUS", "订单或佣金状态无效")
		return
	}
	order, err := s.repo.UpdateAffiliateOrderStatus(r.Context(), r.PathValue("id"), input.OrderStatus, input.CommissionStatus, input.Notes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if order == nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	writeJSON(w, http.StatusOK, flattenedResponse("order", order))
}

func validAffiliateOrderStatus(value string) bool {
	return value == "pending" || value == "completed" || value == "canceled"
}

func validAffiliateCommissionStatus(value string) bool {
	return value == "not_due" || value == "pending" || value == "paid"
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
