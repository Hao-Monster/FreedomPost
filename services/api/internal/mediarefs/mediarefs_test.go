package mediarefs

import (
	"strings"
	"testing"
)

func TestRewriteOnlyDestinationsOutsideCode(t *testing.T) {
	old := "/api/post-media/00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-000000000002"
	next := strings.Replace(old, "000000000001", "000000000003", 1)
	link := "[附件: QA](" + old + ")"
	input := link + "\n\n`" + link + "`\n\n```md\n" + link + "\n```\n\n" + old + "\n\n[ref]: " + old + "\n"
	result := RewriteMarkdown(input, old, next)
	if strings.Count(result, next) != 2 || strings.Count(result, old) != 3 {
		t.Fatalf("code/prose was changed: %s", result)
	}
	html := `<a href="` + old + `">` + old + `</a><code>` + old + `</code>`
	got := RewriteHTML(html, old, next)
	if strings.Count(got, next) != 1 || strings.Count(got, old) != 2 {
		t.Fatalf("HTML text changed: %s", got)
	}
}
