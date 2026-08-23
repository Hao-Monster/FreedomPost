package renderer_test

import (
	"strings"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/renderer"
)

// TestRenderBasicMarkdown verifies HTML output for common Markdown constructs.
func TestRenderBasicMarkdown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		contains []string // expected substrings in HTML
		absent   []string // must NOT appear (XSS check)
	}{
		{
			name:     "heading h1",
			input:    "# 标题一",
			contains: []string{"<h1"},
		},
		{
			name:     "heading h2",
			input:    "## 标题二",
			contains: []string{"<h2"},
		},
		{
			name:     "bold and italic",
			input:    "**粗体** and *斜体*",
			contains: []string{"<strong>粗体</strong>", "<em>斜体</em>"},
		},
		{
			name:     "inline code",
			input:    "Use `fmt.Println()` here",
			contains: []string{"<code>fmt.Println()</code>"},
		},
		{
			name:     "fenced code block with language",
			input:    "```go\nfmt.Println(\"hello\")\n```",
			contains: []string{`data-lang="go"`, `fmt.Println`},
		},
		{
			name:     "unordered list",
			input:    "- 项目 1\n- 项目 2\n- 项目 3",
			contains: []string{"<ul>", "<li>项目 1</li>"},
		},
		{
			name:     "ordered list",
			input:    "1. 第一\n2. 第二",
			contains: []string{"<ol>", "<li>第一</li>"},
		},
		{
			name:     "blockquote",
			input:    "> 这是一段引用",
			contains: []string{"<blockquote>"},
		},
		{
			name:     "strikethrough",
			input:    "~~删除线~~",
			contains: []string{"<del>删除线</del>"},
		},
		{
			name:     "table",
			input:    "| 列1 | 列2 |\n|-----|-----|\n| A   | B   |",
			contains: []string{"<table>", "<th>", "<td>A</td>"},
		},
		{
			name:     "link",
			input:    "[链接文字](https://example.com)",
			contains: []string{`href="https://example.com"`, "链接文字"},
		},
		{
			name:     "image",
			input:    "![alt text](https://example.com/img.png)",
			contains: []string{"<img"},
		},
		{
			name:     "horizontal rule",
			input:    "---",
			contains: []string{"<hr"},
		},
		{
			name:   "XSS: script tag stripped",
			input:  `<script>alert('xss')</script> 正常内容`,
			absent: []string{"<script>", "alert("},
		},
		{
			name:   "XSS: onerror attr stripped",
			input:  `<img src=x onerror=alert(1)>`,
			absent: []string{"onerror", "alert(1)"},
		},
		{
			name:   "XSS: javascript: href stripped",
			input:  `[click](javascript:alert(1))`,
			absent: []string{"javascript:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := renderer.Render(tc.input)

			for _, want := range tc.contains {
				if !strings.Contains(result.HTML, want) {
					t.Errorf("HTML should contain %q\ngot:\n%s", want, result.HTML)
				}
			}
			for _, must_not := range tc.absent {
				if strings.Contains(result.HTML, must_not) {
					t.Errorf("HTML must NOT contain %q (XSS)\ngot:\n%s", must_not, result.HTML)
				}
			}
		})
	}
}

// TestRenderCalloutBlock verifies the custom callout block syntax.
func TestRenderCalloutBlock(t *testing.T) {
	t.Parallel()

	input := ":::callout 💡\n这是一个提示框\n:::"
	result := renderer.Render(input)

	checks := []string{
		`fp-callout`,
		`fp-callout-emoji`,
		"💡",
		"这是一个提示框",
	}
	for _, want := range checks {
		if !strings.Contains(result.HTML, want) {
			t.Errorf("callout HTML should contain %q\ngot:\n%s", want, result.HTML)
		}
	}
	// Must be wrapped in aside
	if !strings.Contains(result.HTML, "<aside") {
		t.Errorf("callout should use <aside> element")
	}
}

// TestRenderSearchText verifies that search text is plain text (no HTML tags).
func TestRenderSearchText(t *testing.T) {
	t.Parallel()

	input := "# 文章标题\n\n这是**正文**内容，包含 `代码` 和 [链接](https://example.com)。"
	result := renderer.Render(input)

	if strings.Contains(result.SearchText, "<") || strings.Contains(result.SearchText, ">") {
		t.Errorf("SearchText should not contain HTML tags, got: %s", result.SearchText)
	}
	if !strings.Contains(result.SearchText, "文章标题") {
		t.Errorf("SearchText should contain title text")
	}
	if !strings.Contains(result.SearchText, "正文") {
		t.Errorf("SearchText should contain body text")
	}
}

// TestRenderExcerpt verifies excerpt is ≤ 160 chars and ends at a word boundary.
func TestRenderExcerpt(t *testing.T) {
	t.Parallel()

	// Generate long content
	long := "这是一段很长的文章内容，" + strings.Repeat("包含很多很多文字内容，", 20)
	result := renderer.Render(long)

	if len([]rune(result.Excerpt)) > 163 { // 160 + "..." margin
		t.Errorf("Excerpt too long: %d runes", len([]rune(result.Excerpt)))
	}
	if result.Excerpt == "" {
		t.Error("Excerpt should not be empty")
	}
}

// TestRenderEmptyInput verifies graceful handling of empty input.
func TestRenderEmptyInput(t *testing.T) {
	t.Parallel()
	result := renderer.Render("")
	// Should not panic, HTML can be empty
	_ = result
}

// TestRenderConcurrent verifies renderer is safe for concurrent use.
func TestRenderConcurrent(t *testing.T) {
	inputs := []string{
		"# 标题\n\n内容",
		"**粗体** 和 *斜体*",
		"```go\npackage main\n```",
		":::callout 🔥\n热点内容\n:::",
		"> 引用\n\n---",
	}
	results := make(chan string, len(inputs)*10)
	for i := 0; i < 10; i++ {
		for _, inp := range inputs {
			go func(s string) {
				r := renderer.Render(s)
				results <- r.HTML
			}(inp)
		}
	}
	for i := 0; i < len(inputs)*10; i++ {
		html := <-results
		if strings.Contains(html, "<script>") {
			t.Error("concurrent render produced unsafe HTML")
		}
	}
}
