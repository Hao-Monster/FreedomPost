// Package renderer converts Markdown to HTML, exactly matching the output of
// the TypeScript @freedompost/renderer package (markdown-it based).
//
// Rendering pipeline:
//  1. Split callout blocks (:::callout ... :::)
//  2. Render each segment with goldmark
//  3. Post-process HTML: attachment cards, YouTube embeds
//  4. Sanitize with bluemonday
//  5. Extract searchText and excerpt
package renderer

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	grenderer "github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// ─── Public API ──────────────────────────────────────────────────────────────

// Result holds the output of a Markdown rendering pass.
type Result struct {
	HTML       string
	SearchText string
	Excerpt    string
}

// Render converts a Markdown string to HTML, search text, and excerpt.
// It is safe for concurrent use.
func Render(markdown string) Result {
	policy := buildPolicy()
	md := buildGoldmark()

	// 1. Split callout blocks
	segments := splitCalloutBlocks(markdown)

	var buf strings.Builder
	for _, seg := range segments {
		switch seg.kind {
		case segMarkdown:
			var out bytes.Buffer
			if err := md.Convert([]byte(seg.content), &out); err != nil {
				buf.WriteString(escapeHTML(seg.content))
			} else {
				buf.Write(out.Bytes())
			}
		case segCallout:
			var inner bytes.Buffer
			if err := md.Convert([]byte(seg.content), &inner); err != nil {
				inner.WriteString(escapeHTML(seg.content))
			}
			fmt.Fprintf(&buf,
				`<aside class="fp-callout" role="note"><span class="fp-callout-emoji" aria-hidden="true">%s</span><div class="fp-callout-content">%s</div></aside>`,
				escapeHTML(seg.emoji), inner.String(),
			)
		}
	}

	// 2. Post-process
	processed := renderAttachmentCards(renderYouTubeEmbeds(buf.String()))

	// 3. Sanitize
	sanitized := policy.Sanitize(processed)

	// 4. Extract text
	searchText := extractSearchText(sanitized)
	excerpt := makeExcerpt(searchText, 160)

	return Result{
		HTML:       sanitized,
		SearchText: searchText,
		Excerpt:    excerpt,
	}
}

// ─── goldmark setup ──────────────────────────────────────────────────────────

func buildGoldmark() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
			extension.Footnote,
		),
		goldmark.WithParserOptions(
			// Custom heading IDs matching TS slugify (Unicode-aware)
			parser.WithASTTransformers(
				util.Prioritized(&headingIDTransformer{usedIDs: map[string]int{}}, 1000),
				util.Prioritized(&externalLinkTransformer{}, 900),
			),
		),
		goldmark.WithRendererOptions(
			// No HardWraps: TS (markdown-it) doesn't convert single newlines to <br>
			ghtml.WithUnsafe(),
		),
		goldmark.WithExtensions(
			newCodeBlockExtension(),
			newImageRendererExtension(),
		),
	)
}

// ─── Code block renderer ─────────────────────────────────────────────────────
// Renders fenced code blocks with line numbers and fold/copy buttons,
// matching the TypeScript fence renderer output.

type codeBlockExtension struct{}

func newCodeBlockExtension() goldmark.Extender { return &codeBlockExtension{} }

func (e *codeBlockExtension) Extend(m goldmark.Markdown) {
	m.Renderer().AddOptions(
		grenderer.WithNodeRenderers(
			util.Prioritized(newCodeBlockRenderer(), 100),
		),
	)
}

type codeBlockRenderer struct{}

func newCodeBlockRenderer() *codeBlockRenderer { return &codeBlockRenderer{} }

func (r *codeBlockRenderer) RegisterFuncs(reg grenderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCode)
}

func (r *codeBlockRenderer) renderFencedCode(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	lang := strings.TrimSpace(string(n.Language(source)))
	if lang == "" {
		lang = "text"
	}

	var content strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		content.Write(line.Value(source))
	}
	code := strings.TrimSuffix(content.String(), "\n")
	lines := strings.Split(code, "\n")

	var sb strings.Builder
	fmt.Fprintf(&sb, `<div class="code-block" data-lang="%s">`, escapeAttr(lang))
	fmt.Fprintf(&sb, `<div class="code-head"><span>%s</span>`, escapeHTML(lang))
	sb.WriteString(`<span class="code-actions">`)
	sb.WriteString(`<button class="fold-code" type="button">折叠</button>`)
	sb.WriteString(`<button class="copy-code" type="button">复制</button>`)
	sb.WriteString(`</span></div>`)
	fmt.Fprintf(&sb, `<pre><code class="language-%s">`, escapeAttr(lang))
	for i, line := range lines {
		fmt.Fprintf(&sb, `<span class="code-line"><span class="line-no">%d</span><span>%s</span></span>`,
			i+1, escapeHTML(line))
	}
	sb.WriteString(`</code></pre></div>`)

	_, err := w.WriteString(sb.String())
	return ast.WalkSkipChildren, err
}

// ─── Heading ID transformer ──────────────────────────────────────────────────
// Generates heading IDs matching the TypeScript slugify algorithm:
//   - lowercase
//   - spaces/underscores → "-"
//   - remove non-Unicode-letter/number chars (keeps Chinese, Arabic, etc.)
//   - collapse multiple dashes, trim leading/trailing
//   - fallback to "section"
//   - deduplicate with -2, -3 suffix

type headingIDTransformer struct {
	usedIDs map[string]int
}

func (t *headingIDTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Kind() != ast.KindHeading {
			return ast.WalkContinue, nil
		}
		h := n.(*ast.Heading)
		var sb strings.Builder
		for c := h.FirstChild(); c != nil; c = c.NextSibling() {
			switch c.Kind() {
			case ast.KindText:
				seg := c.(*ast.Text).Segment
				sb.Write(seg.Value(reader.Source()))
			case ast.KindString:
				sb.Write(c.(*ast.String).Value)
			}
		}
		id := tsUniqueSlug(sb.String(), t.usedIDs)
		h.SetAttribute([]byte("id"), []byte(id))
		return ast.WalkContinue, nil
	})
}

// tsSlugify replicates the TypeScript slugify function from @freedompost/renderer.
func tsSlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	prevDash := false
	for _, r := range s {
		if r == ' ' || r == '_' {
			if !prevDash {
				sb.WriteByte('-')
				prevDash = true
			}
		} else if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' {
			sb.WriteRune(r)
			prevDash = r == '-'
		}
		// else: skip (remove non-letter/number/dash chars)
	}
	result := strings.Trim(sb.String(), "-")
	if result == "" {
		return "section"
	}
	return result
}

func tsUniqueSlug(text string, usedIDs map[string]int) string {
	base := tsSlugify(text)
	seen := usedIDs[base]
	usedIDs[base] = seen + 1
	if seen == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, seen+1)
}

// ─── External link transformer ────────────────────────────────────────────────
// Adds target="_blank" rel="noreferrer noopener" to all external (non-anchor) links,
// matching the TS link_open renderer rule.

type externalLinkTransformer struct{}

func (t *externalLinkTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Kind() != ast.KindLink {
			return ast.WalkContinue, nil
		}
		link := n.(*ast.Link)
		href := string(link.Destination)
		if href != "" && !strings.HasPrefix(href, "#") {
			link.SetAttribute([]byte("target"), []byte("_blank"))
			link.SetAttribute([]byte("rel"), []byte("noreferrer noopener"))
		}
		return ast.WalkContinue, nil
	})
}

// ─── Image renderer extension ─────────────────────────────────────────────────
// Renders images with loading="lazy" and wraps standalone images in an
// <a class="article-image-link"> link, matching the TS image renderer rule.

type imageRendererExtension struct{}
type imageNodeRenderer struct{}

func newImageRendererExtension() goldmark.Extender { return &imageRendererExtension{} }

func (e *imageRendererExtension) Extend(m goldmark.Markdown) {
	m.Renderer().AddOptions(grenderer.WithNodeRenderers(
		// Priority 500 < 1000 (default HTML renderer priority).
		// goldmark sorts node renderers by descending priority, then calls RegisterFuncs in that order.
		// Lower priority = RegisterFuncs called LATER = wins the KindImage registration.
		util.Prioritized(&imageNodeRenderer{}, 500),
	))
}

func (r *imageNodeRenderer) RegisterFuncs(reg grenderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, r.renderImage)
}

func (r *imageNodeRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	img := node.(*ast.Image)
	src := string(img.Destination)
	title := string(img.Title)

	// Collect alt text from child text nodes
	var altBuf strings.Builder
	for c := img.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Kind() == ast.KindText {
			altBuf.Write(c.(*ast.Text).Segment.Value(source))
		} else if c.Kind() == ast.KindString {
			altBuf.Write(c.(*ast.String).Value)
		}
	}
	alt := altBuf.String()

	// Build <img> tag
	var imgTag strings.Builder
	imgTag.WriteString(`<img src="`)
	imgTag.WriteString(escapeAttr(src))
	imgTag.WriteString(`" alt="`)
	imgTag.WriteString(escapeAttr(alt))
	imgTag.WriteString(`"`)
	if title != "" {
		imgTag.WriteString(` title="`)
		imgTag.WriteString(escapeAttr(title))
		imgTag.WriteString(`"`)
	}
	imgTag.WriteString(` loading="lazy">`)

	// Check if already inside a link — walk up the parent chain
	insideLink := false
	for p := img.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == ast.KindLink {
			insideLink = true
			break
		}
	}

	if insideLink || src == "" {
		_, err := w.WriteString(imgTag.String())
		return ast.WalkSkipChildren, err
	}

	// Wrap in <a class="article-image-link">
	wrap := fmt.Sprintf(
		`<a class="article-image-link" href="%s" target="_blank" rel="noreferrer noopener">%s</a>`,
		escapeAttr(src), imgTag.String(),
	)
	_, err := w.WriteString(wrap)
	return ast.WalkSkipChildren, err
}

// ─── bluemonday policy ───────────────────────────────────────────────────────

func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// Allow basic inline elements
	p.AllowElements("p", "br", "hr")
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowElements("ul", "ol", "li")
	p.AllowElements("strong", "em", "s", "mark", "sup", "sub", "del")
	p.AllowElements("blockquote", "pre", "code")
	p.AllowElements("table", "thead", "tbody", "tr", "th", "td")
	p.AllowElements("figure", "figcaption")
	p.AllowElements("input") // for task lists
	p.AllowElements("span", "div", "aside")

	// Headings with id
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")

	// Links
	p.AllowAttrs("href", "target", "rel", "class", "download").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	// Imported and locally uploaded assets are served from /api/uploads. These
	// same-origin paths have no URL scheme, so they must be explicitly allowed
	// or the sanitizer removes an image's src before the post save can claim it.
	p.AllowRelativeURLs(true)

	// Images (title for hover tooltip, matching TS renderer)
	p.AllowAttrs("src", "alt", "title", "loading", "class").OnElements("img")

	// Code blocks (our custom markup — no <button>; TS sanitizer strips buttons)
	p.AllowAttrs("class", "data-lang").OnElements("div", "span", "code", "pre")

	// Task list inputs
	p.AllowAttrs("type", "checked", "disabled", "class").OnElements("input")

	// Table attributes
	p.AllowAttrs("align").OnElements("th", "td")

	// Aside / callout
	p.AllowAttrs("class", "role", "aria-hidden").OnElements("aside", "span", "div")

	// Figure / YouTube
	p.AllowAttrs("class").OnElements("figure", "figcaption")
	p.AllowElements("iframe")
	p.AllowAttrs("src", "title", "loading", "referrerpolicy", "allow", "allowfullscreen", "class").OnElements("iframe")

	// Footnotes
	p.AllowElements("section", "ol")
	p.AllowAttrs("class", "id", "href", "role", "aria-label", "data-footnote-ref", "data-footnote-backref").Matching(bluemonday.Paragraph).OnElements("a", "section", "li", "sup")

	// NOTE: <button> is intentionally NOT allowed — TS sanitizer (sanitizeArticleHtml)
	// strips button elements; buttons in code-block headers appear as plain text.
	// The frontend JavaScript handles fold/copy functionality via click events.

	return p
}

// ─── Callout block splitter ───────────────────────────────────────────────────

type segKind int

const (
	segMarkdown segKind = iota
	segCallout
)

type segment struct {
	kind    segKind
	emoji   string
	content string
}

// calloutStartRe matches two formats:
//
//	:::callout 💡           (space then emoji, used in content)
//	:::callout{emoji="💡"}  (attr syntax, used in old editor)
var calloutStartRe = regexp.MustCompile(`^:::callout(?:\{emoji="([^"]{0,64})"\}|\s+([^\s].*?))?\s*$`)
var fenceRe = regexp.MustCompile(`^\s*(\x60{3,}|~{3,})`)

// normalizeCalloutEmoji accepts any non-empty emoji string.
// Falls back to a default if empty.
func normalizeCalloutEmoji(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "💡" // default emoji
	}
	return v
}

func splitCalloutBlocks(markdown string) []segment {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var segments []segment
	plainStart := 0
	var plainFence *struct {
		marker string
		length int
	}

	for i := 0; i < len(lines); i++ {
		plainFence = nextFence(lines[i], plainFence)
		if plainFence != nil {
			continue
		}
		m := calloutStartRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		// m[1] = attr-syntax emoji, m[2] = space-syntax emoji
		emojiStr := m[1]
		if emojiStr == "" {
			emojiStr = m[2]
		}
		// Find closing :::
		closing := i + 1
		var contentFence *struct {
			marker string
			length int
		}
		for closing < len(lines) {
			contentFence = nextFence(lines[closing], contentFence)
			if contentFence == nil && strings.TrimSpace(lines[closing]) == ":::" {
				break
			}
			closing++
		}
		if closing >= len(lines) {
			continue
		}
		if before := strings.Join(lines[plainStart:i], "\n"); before != "" {
			segments = append(segments, segment{kind: segMarkdown, content: before})
		}
		segments = append(segments, segment{
			kind:    segCallout,
			emoji:   normalizeCalloutEmoji(emojiStr),
			content: strings.Join(lines[i+1:closing], "\n"),
		})
		plainStart = closing + 1
		i = closing
	}
	after := strings.Join(lines[plainStart:], "\n")
	if after != "" || len(segments) == 0 {
		segments = append(segments, segment{kind: segMarkdown, content: after})
	}
	return segments
}

func nextFence(line string, current *struct {
	marker string
	length int
}) *struct {
	marker string
	length int
} {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil {
		return current
	}
	marker := string(m[1][0])
	length := len(m[1])
	if current == nil {
		return &struct {
			marker string
			length int
		}{marker, length}
	}
	if marker == current.marker && length >= current.length {
		return nil
	}
	return current
}

// ─── Post-processing ─────────────────────────────────────────────────────────

var attachmentCardRe = regexp.MustCompile(
	`<p><a href="([^"]+)"(?:\s+target="[^"]*")?(?:\s+rel="[^"]*")?>(?:附件:|附件：)\s*([^<]+)</a></p>`,
)

func renderAttachmentCards(html string) string {
	return attachmentCardRe.ReplaceAllStringFunc(html, func(match string) string {
		sub := attachmentCardRe.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		href := sub[1]
		filename := strings.TrimSpace(sub[2])
		return fmt.Sprintf(
			`<div class="attachment-card"><span>%s</span><a href="%s" target="_blank" rel="noreferrer noopener" download>下载 / 查看</a></div>`,
			escapeHTML(filename), href,
		)
	})
}

var youtubeDirectiveRe = regexp.MustCompile(
	`<p>(::youtube\[[A-Za-z0-9_-]{11}(?:\?start=\d+)?])</p>`,
)
var youtubeDirectiveParseRe = regexp.MustCompile(
	`^::youtube\[([A-Za-z0-9_-]{11})(?:\?start=(\d+))?]$`,
)

func renderYouTubeEmbeds(html string) string {
	return youtubeDirectiveRe.ReplaceAllStringFunc(html, func(match string) string {
		sub := youtubeDirectiveRe.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		parsed := youtubeDirectiveParseRe.FindStringSubmatch(strings.TrimSpace(sub[1]))
		if parsed == nil {
			return match
		}
		videoID := parsed[1]
		start := parsed[2]
		var embedURL, watchURL string
		if start != "" && start != "0" {
			embedURL = fmt.Sprintf("https://www.youtube-nocookie.com/embed/%s?start=%s", videoID, start)
			watchURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s&t=%ss", videoID, start)
		} else {
			embedURL = fmt.Sprintf("https://www.youtube-nocookie.com/embed/%s", videoID)
			watchURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
		}
		return fmt.Sprintf(
			`<figure class="youtube-embed"><div class="youtube-player"><iframe src="%s" title="YouTube 视频播放器" loading="lazy" referrerpolicy="strict-origin-when-cross-origin" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share" allowfullscreen></iframe></div><figcaption><a class="youtube-watch-link" href="%s" target="_blank" rel="noreferrer noopener">在 YouTube 官方页面观看 ↗</a></figcaption></figure>`,
			embedURL, escapeAttr(watchURL),
		)
	})
}

// ─── Search text & excerpt ───────────────────────────────────────────────────

var (
	scriptRe      = regexp.MustCompile(`(?is)<script[\s\S]*?</script>`)
	styleRe       = regexp.MustCompile(`(?is)<style[\s\S]*?</style>`)
	calloutEmojiR = regexp.MustCompile(`(?is)<span\b[^>]*class="[^"]*\bfp-callout-emoji\b[^"]*"[^>]*>[\s\S]*?</span>`)
	tagsRe        = regexp.MustCompile(`<[^>]+>`)
	nbspRe        = regexp.MustCompile(`&nbsp;`)
	spaceRe       = regexp.MustCompile(`\s+`)
)

func extractSearchText(rawHTML string) string {
	s := scriptRe.ReplaceAllString(rawHTML, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = calloutEmojiR.ReplaceAllString(s, " ")
	s = tagsRe.ReplaceAllString(s, " ")
	s = nbspRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func makeExcerpt(text string, maxLen int) string {
	if utf8.RuneCountInString(text) <= maxLen {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maxLen])) + "..."
}

// ─── HTML escaping helpers ────────────────────────────────────────────────────

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#039;")
	return s
}

func escapeAttr(s string) string {
	return strings.ReplaceAll(escapeHTML(s), `"`, "&quot;")
}
