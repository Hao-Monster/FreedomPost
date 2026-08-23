package httpapi

import (
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

// commentSanitizer removes all HTML tags from comment content,
// preventing stored XSS. StrictPolicy keeps plain text only.
var commentSanitizer = bluemonday.StrictPolicy()

// maxCommentRunes is the maximum allowed Unicode character count per comment.
const maxCommentRunes = 5000

// listComments returns all comments for a post.
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
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
	ipKey := security.HashText(ip)

	// Per-day rate limit
	if ok, _ := s.limiter.Allow(r.Context(), "comment-day:"+ipKey, 5, 24*time.Hour); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "今天的评论次数已达上限，请明天再试")
		return
	}
	// Per-5-minute burst limit
	if ok, _ := s.limiter.Allow(r.Context(), "comment-burst:"+ipKey, 3, 5*time.Minute); !ok {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "发送评论太频繁，请稍后再试")
		return
	}

	var input struct {
		ParentID        string `json:"parentId"`
		Content         string `json:"content"`
		FingerprintHash string `json:"fingerprintHash"`
		LocalIDHash     string `json:"localIdHash"`
		Attachments     []struct {
			ID              string `json:"id"`
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

	// Sanitize: strip all HTML tags before any further processing (XSS defence)
	input.Content = commentSanitizer.Sanitize(input.Content)

	// Reject empty or whitespace-only content
	if strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, "CONTENT_EMPTY", "评论内容不能为空")
		return
	}
	// Reject content exceeding the character limit
	if utf8.RuneCountInString(input.Content) > maxCommentRunes {
		writeError(w, http.StatusBadRequest, "CONTENT_TOO_LONG", "评论内容超过最大长度限制")
		return
	}

	// Validate attachment URLs: only allow URLs from configured storage origins
	for _, a := range input.Attachments {
		if !isAllowedAttachmentURL(a.URL, s.cfg.PublicUploadBaseURL, s.cfg.AliyunOSSPublicBaseURL, s.cfg.R2PublicBaseURL) {
			writeError(w, http.StatusBadRequest, "INVALID_ATTACHMENT_URL", "附件URL不合法")
			return
		}
	}

	ipHash := security.HashVisitorKey(ip, s.cfg.VisitorHashSalt)
	username := security.RandomUsername(input.FingerprintHash)

	attachments := make([]domain.PendingAttachment, len(input.Attachments))
	for i, a := range input.Attachments {
		attachments[i] = domain.PendingAttachment{
			ID:              a.ID,
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
		FingerprintHash: input.FingerprintHash,
		LocalIDHash:     input.LocalIDHash,
		Attachments:     attachments,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if comment == nil {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"comment": comment})
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
	s.handleUpload(w, r, "comment", "", "public-comment")
}

// isAllowedAttachmentURL checks that an attachment URL originates from a
// known storage base (local, OSS, R2) or is a relative /api/uploads/ path.
// Empty URLs are allowed (attachment without a public URL).
func isAllowedAttachmentURL(rawURL string, allowedBases ...string) bool {
	if rawURL == "" {
		return true
	}
	// Relative local-storage path is always allowed
	if strings.HasPrefix(rawURL, "/api/uploads/") {
		return true
	}
	for _, base := range allowedBases {
		if base != "" && strings.HasPrefix(rawURL, base) {
			return true
		}
	}
	return false
}

// ─── Upload helper ────────────────────────────────────────────────────────────

// handleUpload processes a multipart file upload and stores it via the storage adapter.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, ownerType, ownerID, uploaderType string) {
	// Limit request body to configured max upload size
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.UploadMaxBytes)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "无效的上传请求")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "NO_FILE", "未找到文件字段")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, s.cfg.UploadMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "READ_ERROR", "读取文件失败")
		return
	}
	if int64(len(data)) > s.cfg.UploadMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "文件超出大小限制")
		return
	}

	obj, err := s.storage.Put(r.Context(), storage.PutInput{
		Data:         data,
		OriginalName: header.Filename,
		OwnerType:    ownerType,
		OwnerID:      ownerID,
		UploaderType: uploaderType,
	})
	if err != nil {
		// Check if it's a validation error (mime type not allowed, etc.)
		writeError(w, http.StatusBadRequest, "UPLOAD_REJECTED", err.Error())
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
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"attachment": att})
}
