package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediaimport"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

const (
	maxImageImportItems       = 50
	imageImportWorkers        = 4
	imageImportRequestsMinute = 4
)

type adminImageImportRequest struct {
	Items []adminImageImportItem `json:"items"`
}

type adminImageImportItem struct {
	ClientID string `json:"clientId"`
	URL      string `json:"url"`
	Name     string `json:"name"`
}

type adminImageImportFailure struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Resolution string `json:"resolution"`
}

type adminImageImportResult struct {
	ClientID string                   `json:"clientId"`
	File     map[string]any           `json:"file,omitempty"`
	Error    *adminImageImportFailure `json:"error,omitempty"`
}

// adminImportPostImages receives only user-selected image sources from a
// privileged editor session. It never forwards that session's credentials to
// the source and stages resulting files until the matching post save claims it.
func (s *Server) adminImportPostImages(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	postID := r.PathValue("id")
	post, err := s.repo.GetPostByID(r.Context(), postID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if post == nil {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}

	if allowed, err := s.limiter.Allow(r.Context(), "admin-image-import:"+security.HashText(s.remoteIP(r)), imageImportRequestsMinute, time.Minute); err != nil {
		s.logger.Warn("admin image import rate limit check failed", "request_id", requestIDFromRequest(r), "error", err)
	} else if !allowed {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "图片转存过于频繁，请稍后再试")
		return
	}

	var request adminImageImportRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.Items) == 0 || len(request.Items) > maxImageImportItems {
		writeImageImportError(w, http.StatusBadRequest, "INVALID_REQUEST", "每次可转存 1 到 50 张图片", "请减少本次粘贴的图片数量后重试")
		return
	}
	seenClientIDs := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		if item.ClientID == "" || len(item.ClientID) > 128 || item.URL == "" || len(item.URL) > 8192 {
			writeImageImportError(w, http.StatusBadRequest, "INVALID_REQUEST", "图片转存请求无效", "请重新粘贴图片，或上传本地图片")
			return
		}
		if _, duplicate := seenClientIDs[item.ClientID]; duplicate {
			writeImageImportError(w, http.StatusBadRequest, "INVALID_REQUEST", "图片转存请求包含重复项目", "请重新粘贴图片后重试")
			return
		}
		seenClientIDs[item.ClientID] = struct{}{}
	}

	s.cleanupExpiredPostImageImports(r.Context())
	results := s.importPostImages(r.Context(), request.Items)
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (s *Server) importPostImages(ctx context.Context, items []adminImageImportItem) []adminImageImportResult {
	results := make([]adminImageImportResult, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := imageImportWorkers
	if len(items) < workerCount {
		workerCount = len(items)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = s.importOnePostImage(ctx, items[index])
			}
		}()
	}
	for index := range items {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results
}

func (s *Server) importOnePostImage(ctx context.Context, item adminImageImportItem) adminImageImportResult {
	result := adminImageImportResult{ClientID: item.ClientID}
	imported, err := (mediaimport.Importer{
		MaxBytes: s.cfg.RemoteImageImportMaxBytes,
		Timeout:  time.Duration(s.cfg.RemoteImageImportTimeoutMS) * time.Millisecond,
	}).Fetch(ctx, item.URL)
	if err != nil {
		result.Error = imageImportFailure(err)
		return result
	}

	claimToken, err := security.NewToken()
	if err != nil {
		s.logger.Error("generate post image import claim", "error", err)
		result.Error = &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "图片转存暂时不可用", Resolution: "请稍后重试，或上传本地图片"}
		return result
	}
	filename := mediaimport.Filename(item.Name, imported.MimeType)
	object, err := s.storage.Put(ctx, storage.PutInput{
		Data:         imported.Data,
		OriginalName: filename,
		OwnerType:    "post",
		UploaderType: "admin-image-import",
	})
	if errors.Is(err, storage.ErrInvalidUpload) {
		result.Error = &adminImageImportFailure{Code: "UNSUPPORTED_IMAGE", Message: "来源内容不是受支持的图片", Resolution: "请使用 JPEG、PNG、GIF、WebP 或 AVIF 图片，或上传本地图片"}
		return result
	}
	if err != nil {
		s.logger.Error("store imported post image", "source_host", imported.Host, "error", err)
		result.Error = &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "图片转存暂时不可用", Resolution: "请稍后重试，或上传本地图片"}
		return result
	}

	stageTTL := s.cfg.RemoteImageImportStageTTL
	if stageTTL <= 0 {
		stageTTL = 24 * time.Hour
	}
	pendingUntil := time.Now().Add(stageTTL)
	attachment, err := s.repo.CreateAttachment(ctx, domain.Attachment{
		OwnerType:        "post",
		OriginalFilename: filename,
		StoredFilename:   object.StoredFilename,
		StorageProvider:  object.StorageProvider,
		StorageKey:       object.StorageKey,
		PublicURL:        object.PublicURL,
		MimeType:         object.MimeType,
		DetectedMime:     object.DetectedMime,
		SizeBytes:        object.SizeBytes,
		SHA256:           object.SHA256,
		ClaimToken:       claimToken,
		ClaimTokenHash:   security.HashToken(claimToken),
		UploaderType:     "admin-image-import",
		PendingUntil:     &pendingUntil,
		SourceHost:       imported.Host,
	})
	if err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cleanupErr := s.storage.Delete(cleanupContext, object.StorageKey); cleanupErr != nil {
			s.logger.Warn("remove orphaned imported image after database failure", "storage_provider", object.StorageProvider, "error", cleanupErr)
		}
		s.logger.Error("persist imported post image", "source_host", imported.Host, "error", err)
		result.Error = &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "图片转存暂时不可用", Resolution: "请稍后重试，或上传本地图片"}
		return result
	}

	result.File = map[string]any{
		"id": attachment.ID, "name": attachment.OriginalFilename, "mimeType": attachment.MimeType,
		"sizeBytes": attachment.SizeBytes, "url": attachment.PublicURL,
		"storageProvider": attachment.StorageProvider, "storageKey": attachment.StorageKey,
		"storedFilename": attachment.StoredFilename, "sha256": attachment.SHA256,
		"claimToken": attachment.ClaimToken,
	}
	return result
}

func imageImportFailure(err error) *adminImageImportFailure {
	var failure *mediaimport.Failure
	if errors.As(err, &failure) {
		return &adminImageImportFailure{Code: failure.Code, Message: failure.Message, Resolution: failure.Resolution}
	}
	return &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "图片转存暂时不可用", Resolution: "请稍后重试，或上传本地图片"}
}

// cleanupExpiredPostImageImports keeps the staging area bounded. Storage
// delete happens first: all adapters make it safe to retry, so a transient DB
// failure leaves a row that can be retried rather than an untracked object.
func (s *Server) cleanupExpiredPostImageImports(ctx context.Context) {
	attachments, err := s.repo.ListExpiredPostImageImports(ctx, 100)
	if err != nil {
		s.logger.Warn("list expired post image imports", "error", err)
		return
	}
	deletedIDs := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if err := s.storage.Delete(ctx, attachment.StorageKey); err != nil {
			s.logger.Warn("remove expired post image import", "attachment_id", attachment.ID, "storage_provider", attachment.StorageProvider, "error", err)
			continue
		}
		deletedIDs = append(deletedIDs, attachment.ID)
	}
	if err := s.repo.DeleteAttachmentsByIDs(ctx, deletedIDs); err != nil {
		s.logger.Warn("delete expired post image import metadata", "count", len(deletedIDs), "error", err)
	}
}

func writeImageImportError(w http.ResponseWriter, status int, code, message, resolution string) {
	writeJSON(w, status, map[string]any{"error": adminImageImportFailure{Code: code, Message: message, Resolution: resolution}})
}
