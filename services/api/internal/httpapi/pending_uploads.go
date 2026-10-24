package httpapi

import (
	"context"
	"os"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
)

func (s *Server) SetManagedStorage(active storage.ManagedAdapter, stores map[string]storage.ManagedAdapter) {
	s.managedStorage, s.managedStores = active, stores
}

// RunPendingUploadCleanup stops with the service. Each batch is bounded and
// failed object deletions retain their database handle for the next pass.
func (s *Server) RunPendingUploadCleanup(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		batch, cancel := context.WithTimeout(ctx, time.Minute)
		s.CleanupPendingAttachments(batch)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) CleanupPendingAttachments(ctx context.Context) {
	attachments, err := s.repo.ClaimExpiredAttachments(ctx, 100)
	if err != nil {
		s.logger.Warn("claim expired uploads", "error", err)
		return
	}
	for _, a := range attachments {
		var adapter storage.Adapter
		if a.DeliveryMode == "managed" {
			adapter = s.managedStores[a.StorageProvider]
		} else if a.StorageProvider == s.cfg.StorageDriver {
			adapter = s.storage
		}
		if adapter == nil {
			s.logger.Warn("expired upload storage unavailable", "provider", a.StorageProvider)
			continue
		}
		if err := adapter.Delete(ctx, a.StorageKey); err != nil && !os.IsNotExist(err) {
			s.logger.Warn("delete expired upload object", "attachment_id", a.ID, "error", err)
			continue
		}
		if err := s.repo.DeleteClaimedAttachment(ctx, a.ID); err != nil {
			s.logger.Warn("delete expired upload metadata", "attachment_id", a.ID, "error", err)
		}
	}
}
