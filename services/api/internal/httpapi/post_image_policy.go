package httpapi

import (
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// unmanagedArticleImageHosts returns the distinct remote hosts used by img
// elements that do not resolve to this deployment's storage. This protects the
// public article surface even if an API client bypasses the paste UI.
func (s *Server) unmanagedArticleImageHosts(renderedHTML string) []string {
	tokenizer := html.NewTokenizer(strings.NewReader(renderedHTML))
	hosts := make(map[string]struct{})
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if !strings.EqualFold(token.Data, "img") {
			continue
		}
		for _, attribute := range token.Attr {
			if !strings.EqualFold(attribute.Key, "src") || isAllowedAttachmentURL(attribute.Val, s.cfg.PublicUploadBaseURL, s.cfg.AliyunOSSPublicBaseURL, s.cfg.R2PublicBaseURL) {
				continue
			}
			parsed, err := url.Parse(attribute.Val)
			if err == nil && parsed.Hostname() != "" {
				hosts[strings.ToLower(parsed.Hostname())] = struct{}{}
			} else {
				hosts["无效图片地址"] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(hosts))
	for host := range hosts {
		result = append(result, host)
	}
	sort.Strings(result)
	return result
}
