package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/microcosm-cc/bluemonday"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

// commentSanitizer removes all HTML tags from comment content,
// preventing stored XSS. StrictPolicy keeps plain text only.
var commentSanitizer = bluemonday.StrictPolicy()

// maxCommentRunes is the maximum allowed Unicode character count per comment.
const (
	maxCommentRunes               = 10000
	maxCommentAttachments         = 5
	publicCommentUploadMaxBytes   = 25 << 20
	multipartRequestOverheadBytes = 1 << 20
)

// listComments returns all comments for a post.
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	ipKey := security.HashText(s.remoteIP(r))
	if ok, _ := s.limiter.Allow(r.Context(), "comment-read:"+ipKey, 180, time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "评论读取过于频繁")
		return
	}
	if !s.requireCommentAccess(w, r) {
		return
	}
	comments, err := s.repo.ListComments(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if comments == nil {
		comments = []domain.Comment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": comments, "comments": comments})
}

// createComment creates a new comment.
// Rate limits: 5 per day per IP, 3 per 5 minutes per IP (matching TS logic).
func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	if ok, _ := s.limiter.Allow(r.Context(), "comment-attempt:"+security.HashText(ip), 60, time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "评论请求过于频繁")
		return
	}
	if !s.requireCommentAccess(w, r) {
		return
	}
	// The subject combines server-observed network identity with bounded client
	// identifiers. Changing only one client field cannot bypass the network key.

	var input struct {
		ParentID    string `json:"parentId"`
		Content     string `json:"content"`
		Fingerprint string `json:"fingerprint"`
		LocalID     string `json:"localId"`
		Attachments []struct {
			ID              string `json:"id"`
			ClaimToken      string `json:"claimToken"`
			Name            string `json:"name"`
			MimeType        string `json:"mimeType"`
			SizeBytes       int64  `json:"sizeBytes"`
			URL             string `json:"url"`
			StorageProvider string `json:"storageProvider"`
			StorageKey      string `json:"storageKey"`
			StoredFilename  string `json:"storedFilename"`
			SHA256          string `json:"sha256"`
		} `json:"attachments"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
	input.LocalID = strings.TrimSpace(input.LocalID)
	if len(input.Fingerprint) > 256 || len(input.LocalID) > 128 || (input.ParentID != "" && uuid.Validate(input.ParentID) != nil) {
		writeError(w, http.StatusBadRequest, "INVALID_COMMENT", "评论标识格式不正确")
		return
	}
	subjectKey := security.HashText(strings.Join([]string{r.PathValue("slug"), ip, input.Fingerprint, input.LocalID}, ":"))
	if ok, _ := s.limiter.Allow(r.Context(), "comment-day:"+subjectKey, 5, 24*time.Hour); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "今天的评论次数已达上限，请明天再试")
		return
	}
	if ok, _ := s.limiter.Allow(r.Context(), "comment-burst:"+subjectKey, 3, 5*time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "发送评论太频繁，请稍后再试")
		return
	}

	// Sanitize: strip all HTML tags before any further processing (XSS defence)
	input.Content = commentSanitizer.Sanitize(input.Content)

	// Reject empty or whitespace-only content
	if strings.TrimSpace(input.Content) == "" && len(input.Attachments) == 0 {
		writeError(w, http.StatusBadRequest, "CONTENT_EMPTY", "评论内容不能为空")
		return
	}
	// Reject content exceeding the character limit
	if utf8.RuneCountInString(input.Content) > maxCommentRunes {
		writeError(w, http.StatusBadRequest, "CONTENT_TOO_LONG", "评论内容超过最大长度限制")
		return
	}

	if len(input.Attachments) > maxCommentAttachments {
		writeError(w, http.StatusBadRequest, "TOO_MANY_ATTACHMENTS", "每条评论最多上传 5 个附件")
		return
	}
	seenAttachments := make(map[string]struct{}, len(input.Attachments))
	for _, a := range input.Attachments {
		if uuid.Validate(a.ID) != nil || len(a.ClaimToken) < 32 || len(a.ClaimToken) > 256 {
			writeError(w, http.StatusBadRequest, "INVALID_ATTACHMENT", "附件认领信息无效")
			return
		}
		if _, exists := seenAttachments[a.ID]; exists {
			writeError(w, http.StatusBadRequest, "INVALID_ATTACHMENT", "附件不能重复提交")
			return
		}
		seenAttachments[a.ID] = struct{}{}
	}

	ipHash := security.HashVisitorKey(ip, s.cfg.VisitorHashSalt)
	seed := input.LocalID
	if seed == "" {
		seed = ip
	}
	username := security.RandomUsername(seed)
	fingerprintHash := ""
	if input.Fingerprint != "" {
		fingerprintHash = security.HashText(input.Fingerprint)
	}
	localIDHash := ""
	if input.LocalID != "" {
		localIDHash = security.HashText(input.LocalID)
	}

	attachments := make([]domain.PendingAttachment, len(input.Attachments))
	for i, a := range input.Attachments {
		attachments[i] = domain.PendingAttachment{
			ID:              a.ID,
			ClaimToken:      a.ClaimToken,
			Name:            a.Name,
			MimeType:        a.MimeType,
			SizeBytes:       a.SizeBytes,
			URL:             a.URL,
			StorageProvider: a.StorageProvider,
			StorageKey:      a.StorageKey,
			StoredFilename:  a.StoredFilename,
			SHA256:          a.SHA256,
		}
	}

	comment, err := s.repo.CreateComment(r.Context(), domain.CreateCommentInput{
		PostSlug:        r.PathValue("slug"),
		ParentID:        input.ParentID,
		Username:        username,
		Content:         input.Content,
		IPHash:          ipHash,
		FingerprintHash: fingerprintHash,
		LocalIDHash:     localIDHash,
		Attachments:     attachments,
	})
	if errors.Is(err, domain.ErrInvalidAttachment) {
		writeError(w, http.StatusBadRequest, "INVALID_ATTACHMENT", "附件无效、已被使用或不属于本次上传")
		return
	}
	if errors.Is(err, domain.ErrInvalidState) {
		writeError(w, http.StatusBadRequest, "INVALID_PARENT", "回复的上级评论不存在")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if comment == nil {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	writeJSON(w, http.StatusCreated, flattenedResponse("comment", comment))
}

// uploadCommentAttachment handles public comment file uploads.
// Rate limits: 10 per hour per IP.
func (s *Server) uploadCommentAttachment(w http.ResponseWriter, r *http.Request) {
	ip := s.remoteIP(r)
	ipKey := security.HashText(ip)
	if ok, _ := s.limiter.Allow(r.Context(), "comment-upload:"+ipKey, 10, time.Hour); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "上传附件太频繁，请稍后再试")
		return
	}
	if !s.requireCommentAccess(w, r) {
		return
	}
	maxBytes := s.cfg.UploadMaxBytes
	if maxBytes > publicCommentUploadMaxBytes {
		maxBytes = publicCommentUploadMaxBytes
	}
	s.handleUpload(w, r, "comment", "", "public-comment", maxBytes)
}

func (s *Server) requireCommentAccess(w http.ResponseWriter, r *http.Request) bool {
	return s.requireReadablePost(w, r) != nil
}

// isAllowedAttachmentURL checks that an attachment URL originates from a
// known storage base (local, OSS, R2) or is a relative /api/uploads/ path.
// Empty URLs are allowed (attachment without a public URL).
func isAllowedAttachmentURL(rawURL string, allowedBases ...string) bool {
	if rawURL == "" {
		return true
	}
	// Relative local-storage path is always allowed
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(path.Clean(parsed.Path), "/api/uploads/") {
		return true
	}
	for _, base := range allowedBases {
		baseURL, err := url.Parse(strings.TrimRight(base, "/"))
		if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
			continue
		}
		basePath := strings.TrimRight(path.Clean(baseURL.Path), "/")
		candidatePath := path.Clean(parsed.Path)
		pathMatches := candidatePath == basePath || strings.HasPrefix(candidatePath, basePath+"/")
		if parsed.Scheme == baseURL.Scheme && parsed.Host == baseURL.Host && pathMatches {
			return true
		}
	}
	return false
}

// ─── Upload helper ────────────────────────────────────────────────────────────

// handleUpload processes a multipart file upload and stores it via the storage adapter.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, ownerType, ownerID, uploaderType string, maxBytes int64) {
	// Limit request body to configured max upload size
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+multipartRequestOverheadBytes)

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "无效的上传请求")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "NO_FILE", "未找到文件字段")
		return
	}
	defer file.Close()
	if header.Size > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "文件超出大小限制")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "READ_ERROR", "读取文件失败")
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "文件超出大小限制")
		return
	}

	// Generate the one-time claim before persisting bytes so an entropy-source
	// failure cannot leave an object with no database row or cleanup handle.
	claimToken := ""
	claimTokenHash := ""
	if uploaderType == "public-comment" {
		claimToken, err = security.NewToken()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		claimTokenHash = security.HashToken(claimToken)
	}

	obj, err := s.storage.Put(r.Context(), storage.PutInput{
		Data:         data,
		OriginalName: header.Filename,
		OwnerType:    ownerType,
		OwnerID:      ownerID,
		UploaderType: uploaderType,
	})
	if errors.Is(err, storage.ErrInvalidUpload) {
		writeError(w, http.StatusBadRequest, "UPLOAD_REJECTED", "文件类型或大小不符合要求")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	// Persist attachment metadata to DB
	att, err := s.repo.CreateAttachment(r.Context(), domain.Attachment{
		OwnerType:        ownerType,
		OriginalFilename: header.Filename,
		StoredFilename:   obj.StoredFilename,
		StorageProvider:  obj.StorageProvider,
		StorageKey:       obj.StorageKey,
		PublicURL:        obj.PublicURL,
		MimeType:         obj.MimeType,
		DetectedMime:     obj.DetectedMime,
		SizeBytes:        obj.SizeBytes,
		SHA256:           obj.SHA256,
		ClaimToken:       claimToken,
		ClaimTokenHash:   claimTokenHash,
		UploaderType:     uploaderType,
	})
	if err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cleanupErr := s.storage.Delete(cleanupContext, obj.StorageKey); cleanupErr != nil {
			s.logger.Warn("remove orphaned upload after database failure", "storage_provider", obj.StorageProvider, "error", cleanupErr)
		}
		s.internalError(w, r, err)
		return
	}

	fileResponse := map[string]any{
		"id": att.ID, "name": att.OriginalFilename, "mimeType": att.MimeType,
		"sizeBytes": att.SizeBytes, "url": att.PublicURL,
		"storageProvider": att.StorageProvider, "storageKey": att.StorageKey,
		"storedFilename": att.StoredFilename, "sha256": att.SHA256,
		"claimToken": att.ClaimToken,
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": att, "file": fileResponse})
}
