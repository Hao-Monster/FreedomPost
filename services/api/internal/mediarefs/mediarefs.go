// Package mediarefs recognizes only the application's article-scoped media
// URLs. It does not infer attachment ownership from arbitrary remote URLs.
package mediarefs

import (
	"net/url"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

const Prefix = "/api/post-media/"

// LegacyIdentity identifies the object behind query/fragment variants. Only
// the legacy local upload route is host-independent; remote stores keep host.
func LegacyIdentity(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	clean := path.Clean(u.Path)
	if strings.HasPrefix(clean, "/api/uploads/") {
		return clean
	}
	if u.Host == "" {
		return clean
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && port != "443" && port != "80" {
		host += ":" + port
	}
	return host + clean
}

func Filename(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return path.Base(path.Clean(u.Path))
}

func Parse(raw string) (postID, attachmentID string, ok bool) {
	if !strings.HasPrefix(raw, Prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(raw, Prefix), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	for _, value := range parts {
		id, err := uuid.Parse(value)
		if err != nil || id.String() != value {
			return "", "", false
		}
	}
	return parts[0], parts[1], true
}

// RewriteMarkdown changes destinations emitted by the editor (plus ordinary
// reference definitions), while preserving identical text inside code blocks
// and inline code. It deliberately does not replace arbitrary prose URLs.
func RewriteMarkdown(markdown, from, to string) string {
	type span struct{ start, end int }
	var code []span
	doc := goldmark.New().Parser().Parse(text.NewReader([]byte(markdown)))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.FencedCodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				s := n.Lines().At(i)
				code = append(code, span{s.Start, s.Stop})
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				s := n.Lines().At(i)
				code = append(code, span{s.Start, s.Stop})
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				if s, ok := child.(*ast.Text); ok {
					code = append(code, span{s.Segment.Start, s.Segment.Stop})
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	var out strings.Builder
	position := 0
	for position < len(markdown) {
		offset := strings.Index(markdown[position:], from)
		if offset < 0 {
			out.WriteString(markdown[position:])
			break
		}
		index := position + offset
		end := index + len(from)
		insideCode := false
		for _, s := range code {
			if index >= s.start && index < s.end {
				insideCode = true
				break
			}
		}
		before := markdown[:index]
		after := markdown[end:]
		destination := strings.HasSuffix(before, "](") && (strings.HasPrefix(after, ")") || strings.HasPrefix(after, " ") || strings.HasPrefix(after, "\t")) || strings.HasSuffix(before, "](<") && strings.HasPrefix(after, ">")
		for _, attr := range []string{"src", "href", "poster"} {
			destination = destination || strings.HasSuffix(before, attr+`="`) && strings.HasPrefix(after, `"`) || strings.HasSuffix(before, attr+"='") && strings.HasPrefix(after, "'")
		}
		line := strings.TrimSpace(before[strings.LastIndex(before, "\n")+1:])
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]:") {
			tail := strings.TrimSpace(line[strings.Index(line, "]:")+2:])
			destination = destination || tail == "" || tail == "<"
		}
		out.WriteString(markdown[position:index])
		if destination && !insideCode {
			out.WriteString(to)
		} else {
			out.WriteString(from)
		}
		position = end
	}
	return out.String()
}

func RewriteHTML(rendered, from, to string) string {
	tokens := html.NewTokenizer(strings.NewReader(rendered))
	var out strings.Builder
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		raw := string(tokens.Raw())
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			token := tokens.Token()
			changed := false
			for i, attr := range token.Attr {
				if (attr.Key == "src" || attr.Key == "href" || attr.Key == "poster") && attr.Val == from {
					token.Attr[i].Val = to
					changed = true
				}
			}
			if changed {
				raw = token.String()
			}
		}
		out.WriteString(raw)
	}
	return out.String()
}

// URLs returns actual rendered media/link destinations, not text inside code
// blocks. The server-rendered HTML is already sanitized before this is called.
func URLs(rendered string) []string {
	var result []string
	seen := map[string]bool{}
	tokens := html.NewTokenizer(strings.NewReader(rendered))
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		for _, attr := range token.Attr {
			if (attr.Key == "src" || attr.Key == "href" || attr.Key == "poster") && !seen[attr.Val] {
				seen[attr.Val] = true
				result = append(result, attr.Val)
			}
		}
	}
	return result
}

// ObjectURLs excludes ordinary navigation links when auditing media without
// metadata. Download-card links and embedded image/audio/video sources count.
func ObjectURLs(rendered string) []string {
	doc, err := html.Parse(strings.NewReader(rendered))
	if err != nil {
		return nil
	}
	var result []string
	var visit func(*html.Node, bool)
	visit = func(node *html.Node, inCard bool) {
		if node.Type == html.ElementNode {
			for _, attr := range node.Attr {
				if attr.Key == "class" && strings.Contains(" "+attr.Val+" ", " attachment-card ") {
					inCard = true
				}
			}
			for _, attr := range node.Attr {
				if (node.Data == "img" || node.Data == "audio" || node.Data == "video" || node.Data == "source") && (attr.Key == "src" || attr.Key == "poster") || node.Data == "a" && inCard && attr.Key == "href" {
					result = append(result, attr.Val)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inCard)
		}
	}
	visit(doc, false)
	return result
}
