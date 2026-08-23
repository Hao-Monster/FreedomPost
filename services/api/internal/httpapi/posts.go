package httpapi

import (
	"net/http"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/searchindex"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

// listPosts returns all public posts as a list of PostSummary items.
func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := s.repo.ListPostSummaries(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if posts == nil {
		posts = []domain.PostSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": posts, "posts": posts})
}

// getPost returns a single post by slug.
// For paid posts, HTML content is omitted unless the reader has access
// (delegated to paid-access via the frontend).
func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	post, err := s.repo.GetPostBySlug(r.Context(), slug)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if post == nil || post.Visibility == domain.VisibilityPrivate {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": post, "post": post})
}

// recordView records a page view and returns the updated view count.
// Uses Redis buffer for high-throughput write avoidance.
func (s *Server) recordView(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	var input struct {
		ViewDate        string `json:"viewDate"` // YYYY-MM-DD
		VisitorKey      string `json:"visitorKey"`
		FingerprintHash string `json:"fingerprintHash"`
		LocalIDHash     string `json:"localIdHash"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	ip := s.remoteIP(r)
	ipHash := security.HashVisitorKey(ip, s.cfg.VisitorHashSalt)
	visitorKey := input.VisitorKey
	if visitorKey == "" {
		visitorKey = ipHash
	}

	result, err := s.repo.RecordView(r.Context(), domain.RecordViewInput{
		PostSlug:        slug,
		ViewDate:        input.ViewDate,
		VisitorKey:      visitorKey,
		IPHash:          ipHash,
		FingerprintHash: input.FingerprintHash,
		LocalIDHash:     input.LocalIDHash,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if result == nil {
		writeError(w, http.StatusNotFound, "POST_NOT_FOUND", "文章不存在")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// searchIndex returns the pre-built search index payload.
// The payload matches @freedompost/shared SearchIndexPayload exactly.
func (s *Server) searchIndex(w http.ResponseWriter, r *http.Request) {
	data, err := s.searchCache.GetOrBuild(func() ([]searchindex.SearchDocument, error) {
		docs, err := s.repo.GetSearchDocuments(r.Context())
		if err != nil {
			return nil, err
		}
		result := make([]searchindex.SearchDocument, len(docs))
		for i, d := range docs {
			result[i] = searchindex.SearchDocument{
				ID:        d.ID,
				Slug:      d.Slug,
				Title:     d.Title,
				Body:      d.Body,
				Excerpt:   d.Excerpt,
				UpdatedAt: d.UpdatedAt,
			}
		}
		return result, nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// listProducts returns the public product list (published only).
func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	products, err := s.repo.ListProducts(r.Context(), true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if products == nil {
		products = []domain.Product{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": products, "products": products})
}

// getProduct returns a single published product by slug.
func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	product, err := s.repo.GetProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if product == nil || product.Status != "published" {
		writeError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": product, "product": product})
}

// listTools returns the public tool list (published only).
func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	tools, err := s.repo.ListTools(r.Context(), true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tools == nil {
		tools = []domain.Tool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tools, "tools": tools})
}

// serveUpload serves a locally stored file with path traversal protection.
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	if s.localStore == nil {
		writeError(w, http.StatusNotFound, "UPLOAD_NOT_FOUND", "文件不存在")
		return
	}
	s.localStore.ServeFile(w, r, r.PathValue("path"))
}
