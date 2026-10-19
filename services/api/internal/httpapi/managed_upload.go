package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

const (
	commentPendingIPBytes     int64 = 100 << 20
	commentPendingGlobalBytes int64 = 1 << 30
)

func (s *Server) persistManagedUpload(w http.ResponseWriter, r *http.Request, data []byte, filename, ownerType, uploaderType, postID string, maxBytes int64) {
	mimeType, err := storage.Validate(data, maxBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "UPLOAD_REJECTED", "文件类型或大小不符合要求")
		return
	}
	claimToken, err := security.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	storedFilename := storage.GenerateStoredFilename(filename, mimeType)
	key, err := s.managedStorage.KeyForFilename(storedFilename)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	expires := time.Now().Add(24 * time.Hour)
	attachment := domain.Attachment{
		OwnerType: ownerType, UploaderType: uploaderType, ContextPostID: &postID,
		DeliveryMode: "managed", UploadState: "uploading", OriginalFilename: filename,
		StoredFilename: storedFilename, StorageProvider: s.managedStorage.Provider(), StorageKey: key,
		MimeType: mimeType, DetectedMime: mimeType, SizeBytes: int64(len(data)), SHA256: storage.ComputeSHA256(data),
		ClaimToken: claimToken, ClaimTokenHash: security.HashToken(claimToken), PendingUntil: &expires,
	}
	var saved *domain.Attachment
	if uploaderType == "public-comment" {
		attachment.UploaderIPHash = security.HashVisitorKey(s.remoteIP(r), s.cfg.VisitorHashSalt)
		saved, err = s.repo.ReserveCommentAttachment(r.Context(), attachment, commentPendingIPBytes, commentPendingGlobalBytes)
	} else {
		saved, err = s.repo.CreateAttachment(r.Context(), attachment)
	}
	if errors.Is(err, domain.ErrUploadQuota) {
		writeError(w, http.StatusTooManyRequests, "UPLOAD_QUOTA", "未提交附件数量过多，请稍后再试")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.managedStorage.Put(r.Context(), storage.PutInput{Data: data, OriginalName: filename, StoredFilename: storedFilename, OwnerType: ownerType, UploaderType: uploaderType})
	if err == nil {
		err = s.repo.CompleteAttachmentUpload(r.Context(), saved.ID)
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if abortErr := s.repo.AbortAttachmentUpload(cleanup, saved.ID); abortErr != nil {
			s.logger.Warn("abort managed upload", "attachment_id", saved.ID, "error", abortErr)
		} else {
			s.CleanupPendingAttachments(cleanup)
		}
		s.internalError(w, r, err)
		return
	}
	saved.UploadState = "ready"
	saved.PublicURL = domain.ManagedMediaURL(postID, saved.ID)
	saved.ClaimToken = claimToken
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": saved, "file": map[string]any{
		"id": saved.ID, "name": saved.OriginalFilename, "mimeType": saved.MimeType, "sizeBytes": saved.SizeBytes,
		"url": saved.PublicURL, "claimToken": claimToken,
	}})
}
