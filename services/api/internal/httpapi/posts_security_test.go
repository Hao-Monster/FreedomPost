package httpapi

import (
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

func TestRedactPostForPublicRemovesEveryPaidContentField(t *testing.T) {
	original := &domain.Post{
		Visibility: domain.VisibilityPaid, ContentMarkdown: "markdown-secret",
		Markdown: "legacy-secret", ContentHTML: "html-secret",
		SearchText: "search-secret", Excerpt: "excerpt-secret", AttachmentCount: 3,
	}
	redacted := redactPostForPublic(original)
	if redacted.ContentMarkdown != "" || redacted.Markdown != "" || redacted.ContentHTML != "" ||
		redacted.SearchText != "" || redacted.Excerpt != "" || redacted.AttachmentCount != 0 {
		t.Fatalf("paid post was not fully redacted: %+v", redacted)
	}
	if original.ContentHTML != "html-secret" || original.Excerpt != "excerpt-secret" {
		t.Fatal("redaction mutated the repository object")
	}
}

func TestRedactPostForPublicKeepsPublicContent(t *testing.T) {
	original := &domain.Post{Visibility: domain.VisibilityPublic, ContentHTML: "public"}
	if got := redactPostForPublic(original); got != original || got.ContentHTML != "public" {
		t.Fatalf("public post changed: %+v", got)
	}
}
