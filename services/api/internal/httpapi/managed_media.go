package httpapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediarefs"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

func (s *Server) adminUploadPostAttachment(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	post, err := s.repo.GetPostByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if post == nil {
		http.NotFound(w, r)
		return
	}
	s.handleUpload(w, r, "post", "", "admin", s.cfg.UploadMaxBytes, post.ID)
}

func (s *Server) hasAdminMediaSession(r *http.Request) bool {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" || s.sessions == nil {
		return false
	}
	session, err := s.sessions.GetAdminSession(r.Context(), security.HashToken(cookie.Value))
	return err == nil && session != nil
}

func (s *Server) serveManagedMedia(w http.ResponseWriter, r *http.Request) {
	// This applies even to denials, HEAD and ranges. Public articles can become
	// private without waiting for a CDN/browser cache or signed URL to expire.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	postID, attachmentID, valid := mediarefs.Parse(domain.ManagedMediaURL(r.PathValue("postID"), r.PathValue("attachmentID")))
	if !valid {
		http.NotFound(w, r)
		return
	}
	attachment, err := s.repo.GetManagedAttachment(r.Context(), attachmentID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if attachment == nil || attachment.DeliveryMode != "managed" || attachment.UploadState != "ready" {
		http.NotFound(w, r)
		return
	}
	post, err := s.repo.GetPostByID(r.Context(), postID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if post == nil {
		http.NotFound(w, r)
		return
	}
	isAdmin := s.hasAdminMediaSession(r)
	pending := attachment.PendingUntil != nil
	if pending {
		// Staged media is never readable by an anonymous commenter, even for a
		// public article. The editor can preview only its article's live upload.
		if !isAdmin || attachment.ContextPostID == nil || *attachment.ContextPostID != postID || !attachment.PendingUntil.After(time.Now()) {
			http.NotFound(w, r)
			return
		}
	} else {
		referenced, err := s.repo.HasPostAttachmentReference(r.Context(), postID, attachmentID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !referenced {
			http.NotFound(w, r)
			return
		}
	}
	if !isAdmin {
		switch post.Visibility {
		case domain.VisibilityPublic:
		case domain.VisibilityPaid:
			cookie, err := r.Cookie(readerCookieName)
			if err != nil || cookie.Value == "" || s.paidAccess == nil {
				http.NotFound(w, r)
				return
			}
			allowed, err := s.paidAccess.CheckArticleAccess(r.Context(), cookie.Value, post.Slug)
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			if !allowed {
				http.NotFound(w, r)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
	}
	store := s.managedStores[attachment.StorageProvider]
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "MEDIA_UNAVAILABLE", "附件暂时不可用")
		return
	}
	object, err := store.Open(r.Context(), attachment.StorageKey, r.Method, r.Header.Get("Range"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "MEDIA_UNAVAILABLE", "附件暂时不可用")
		return
	}
	if object.Body != nil {
		defer object.Body.Close()
	}
	w.Header().Set("Content-Type", attachment.MimeType)
	w.Header().Set("Accept-Ranges", "bytes")
	if !strings.HasPrefix(attachment.MimeType, "image/") && !strings.HasPrefix(attachment.MimeType, "audio/") && !strings.HasPrefix(attachment.MimeType, "video/") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.OriginalFilename}))
	}
	if object.ContentRange != "" {
		w.Header().Set("Content-Range", object.ContentRange)
	}
	if object.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(object.ContentLength, 10))
	}
	w.WriteHeader(object.StatusCode)
	if r.Method != http.MethodHead && object.Body != nil {
		_, _ = io.Copy(w, object.Body)
	}
}
