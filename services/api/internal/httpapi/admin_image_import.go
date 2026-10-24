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

	s.CleanupPendingAttachments(r.Context())
	results := s.importPostImages(r.Context(), postID, request.Items)
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (s *Server) importPostImages(ctx context.Context, postID string, items []adminImageImportItem) []adminImageImportResult {
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
				results[index] = s.importOnePostImage(ctx, postID, items[index])
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

func (s *Server) importOnePostImage(ctx context.Context, postID string, item adminImageImportItem) adminImageImportResult {
	result := adminImageImportResult{ClientID: item.ClientID}
	if s.managedStorage == nil || postID == "" {
		result.Error = &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "附件存储尚未配置", Resolution: "请联系管理员完成私有附件存储配置"}
		return result
	}
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
	storedName := storage.GenerateStoredFilename(filename, imported.MimeType)
	key, err := s.managedStorage.KeyForFilename(storedName)
	if err != nil {
		result.Error = imageImportFailure(err)
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
		StoredFilename:   storedName,
		StorageProvider:  s.managedStorage.Provider(),
		StorageKey:       key,
		PublicURL:        "",
		DeliveryMode:     "managed",
		ContextPostID:    &postID,
		UploadState:      "uploading",
		MimeType:         imported.MimeType,
		DetectedMime:     imported.MimeType,
		SizeBytes:        int64(len(imported.Data)),
		SHA256:           storage.ComputeSHA256(imported.Data),
		ClaimToken:       claimToken,
		ClaimTokenHash:   security.HashToken(claimToken),
		UploaderType:     "admin-image-import",
		PendingUntil:     &pendingUntil,
		SourceHost:       imported.Host,
	})
	if err != nil {
		s.logger.Error("persist imported post image", "source_host", imported.Host, "error", err)
		result.Error = &adminImageImportFailure{Code: "IMPORT_UNAVAILABLE", Message: "图片转存暂时不可用", Resolution: "请稍后重试，或上传本地图片"}
		return result
	}
	_, err = s.managedStorage.Put(ctx, storage.PutInput{Data: imported.Data, OriginalName: filename, StoredFilename: storedName, OwnerType: "post", UploaderType: "admin-image-import"})
	if err == nil {
		err = s.repo.CompleteAttachmentUpload(ctx, attachment.ID)
	}
	if err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.AbortAttachmentUpload(cleanupContext, attachment.ID)
		s.CleanupPendingAttachments(cleanupContext)
		result.Error = imageImportFailure(err)
		return result
	}

	result.File = map[string]any{
		"id": attachment.ID, "name": attachment.OriginalFilename, "mimeType": attachment.MimeType,
		"sizeBytes": attachment.SizeBytes, "url": domain.ManagedMediaURL(postID, attachment.ID),
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

func writeImageImportError(w http.ResponseWriter, status int, code, message, resolution string) {
	writeJSON(w, status, map[string]any{"error": adminImageImportFailure{Code: code, Message: message, Resolution: resolution}})
}
