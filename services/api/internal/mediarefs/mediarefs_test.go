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

func TestRewriteMarkdownHandlesPaddedLinksAndEscapedHTMLAttrs(t *testing.T) {
	oldURL := "/api/uploads/sample.png?x=1&y=2"
	newURL := "/api/post-media/00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-000000000002"
	markdown := "![padded]( " + oldURL + " )\n\n" +
		`<img SRC = "` + strings.ReplaceAll(oldURL, "&", "&amp;") + `">` + "\n\n" +
		"`![inline]( " + oldURL + " )`"
	got := RewriteMarkdown(markdown, oldURL, newURL)
	if strings.Count(got, newURL) != 2 {
		t.Fatalf("expected 2 rewritten destinations, got: %s", got)
	}
	if !strings.Contains(got, "`![inline]( "+oldURL+" )`") {
		t.Fatalf("inline code should remain untouched: %s", got)
	}
}

func TestSingleSlashAndBackslashSpecialURLHasCanonicalIdentity(t *testing.T) {
	canonical := "http://reader.example/api/uploads/qa.txt"
	for _, raw := range []string{
		"http:/reader.example/api/uploads/qa.txt",
		`http:\reader.example\api\uploads\qa.txt`,
		"https:/api/uploads/qa.txt",
		`https:\api\uploads\qa.txt`,
		"https:\\\\reader.example\\api\\uploads\\qa.txt",
		"http:///reader.example/api/uploads/qa.txt",
	} {
		parsed, err := ParseURL(raw)
		if err != nil {
			t.Fatal(err)
		}
		if LegacyIdentity(raw) != LegacyIdentity(canonical) {
			t.Errorf("browser-equivalent URL not recognized: raw=%q host=%q path=%q identity=%q expected=%q", raw, parsed.Host, parsed.Path, LegacyIdentity(raw), LegacyIdentity(canonical))
		}
	}
}
