package httpapi

import (
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediarefs"

	"golang.org/x/net/html"
)

// unmanagedArticleImageHosts returns the distinct remote hosts used by img
// elements that do not resolve to this deployment's storage. This protects the
// public article surface even if an API client bypasses the paste UI.
func (s *Server) unmanagedArticleImageHosts(renderedHTML string) []string {
	tokenizer := html.NewTokenizer(strings.NewReader(renderedHTML))
	hosts := make(map[string]struct{})
	ossBaseURL := s.cfg.AliyunOSSPublicBaseURL
	if ossBaseURL == "" && s.cfg.AliyunOSSBucket != "" && s.cfg.AliyunOSSRegion != "" {
		ossBaseURL = "https://" + s.cfg.AliyunOSSBucket + ".oss-" + s.cfg.AliyunOSSRegion + ".aliyuncs.com"
	}
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
			if _, _, valid := mediarefs.Parse(attribute.Val); attribute.Key == "src" && valid {
				continue
			}
			if !strings.EqualFold(attribute.Key, "src") || isAllowedAttachmentURL(attribute.Val, s.cfg.PublicUploadBaseURL, ossBaseURL, s.cfg.R2PublicBaseURL) {
				continue
			}
			parsed, err := mediarefs.ParseURL(attribute.Val)
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

// Every media reference to a configured public object origin remains public,
// even if its metadata is missing or a query/fragment aliases the old object.
func (s *Server) hasPublicArticleMedia(r *http.Request, rendered string) bool {
	bases := []string{s.cfg.PublicUploadBaseURL, s.cfg.R2PublicBaseURL, s.cfg.AliyunOSSPublicBaseURL}
	if s.cfg.AliyunOSSBucket != "" && s.cfg.AliyunOSSRegion != "" {
		bases = append(bases, "https://"+s.cfg.AliyunOSSBucket+".oss-"+s.cfg.AliyunOSSRegion+".aliyuncs.com")
	}
	site, _ := mediarefs.ParseURL(s.cfg.PublicSiteURL)
	requestOrigin, _ := mediarefs.ParseURL("http://" + r.Host)
	for _, raw := range mediarefs.URLs(rendered) {
		if _, _, ok := mediarefs.Parse(raw); ok {
			continue
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		candidate, err := mediarefs.ParseURL(raw)
		if err != nil {
			return true
		}
		candidates := []*url.URL{candidate}
		if candidate.Host == "" && (candidate.Scheme == "http" || candidate.Scheme == "https") && strings.HasPrefix(candidate.Path, "/") {
			if alt, altErr := url.Parse(candidate.Scheme + "://" + strings.TrimLeft(candidate.Path, "/")); altErr == nil {
				candidates = append(candidates, alt)
			}
		}
		for _, candidate := range candidates {
			ownHost := candidate.Host == "" || requestOrigin != nil && strings.EqualFold(candidate.Hostname(), requestOrigin.Hostname()) || site != nil && strings.EqualFold(candidate.Hostname(), site.Hostname())
			candidatePath := path.Clean(candidate.Path)
			if ownHost && strings.HasPrefix(candidatePath, "/api/uploads/") {
				return true
			}
			for _, base := range bases {
				if base == "" {
					continue
				}
				configured, err := mediarefs.ParseURL(base)
				if err != nil {
					continue
				}
				basePath := strings.TrimRight(path.Clean("/"+configured.Path), "/")
				if configured.Host != "" && !strings.EqualFold(candidate.Hostname(), configured.Hostname()) {
					continue
				}
				if configured.Host == "" && !ownHost {
					continue
				}
				if candidatePath == basePath || strings.HasPrefix(candidatePath, basePath+"/") {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) normalizeManagedArticleReferences(r *http.Request, markdown, rendered string) string {
	site, _ := mediarefs.ParseURL(s.cfg.PublicSiteURL)
	requestOrigin, _ := mediarefs.ParseURL("http://" + r.Host)
	for _, raw := range mediarefs.URLs(rendered) {
		candidate, err := mediarefs.ParseURL(raw)
		if err != nil || candidate.Host == "" || (candidate.Scheme != "" && candidate.Scheme != "http" && candidate.Scheme != "https") {
			continue
		}
		ownHost := requestOrigin != nil && articleOriginHost(candidate) == articleOriginHost(requestOrigin) || site != nil && articleOriginHost(candidate) == articleOriginHost(site)
		if _, _, ok := mediarefs.Parse(candidate.Path); ownHost && ok {
			markdown = mediarefs.RewriteMarkdown(markdown, raw, candidate.Path)
		}
	}
	return markdown
}

func articleOriginHost(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && port != "80" && port != "443" {
		host += ":" + port
	}
	return host
}
