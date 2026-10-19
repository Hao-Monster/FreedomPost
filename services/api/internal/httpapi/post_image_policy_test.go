package httpapi

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
)

func TestUnmanagedArticleImageHosts(t *testing.T) {
	server := &Server{cfg: &config.Config{R2PublicBaseURL: "https://assets.example/freedompost/uploads"}}
	hosts := server.unmanagedArticleImageHosts(`<p><img src="/api/uploads/a.png"><img src="https://assets.example/freedompost/uploads/b.png"><img src="https://expired.feishu.cn/image.png"><img src="https://expired.feishu.cn/again.png"></p>`)
	if want := []string{"expired.feishu.cn"}; !reflect.DeepEqual(hosts, want) {
		t.Fatalf("hosts = %#v, want %#v", hosts, want)
	}
}

func TestExistingArticleTextEditWithRootStorageBase(t *testing.T) {
	for _, base := range []string{"https://r2pic.openal.uk", "https://r2pic.openal.uk/"} {
		t.Run(base, func(t *testing.T) {
			server := &Server{cfg: &config.Config{R2PublicBaseURL: base}}
			markdown := "安装的时候选English，之后就都是中文了\n\n![图片](https://r2pic.openal.uk/freedompost/uploads/admin/2026/08/15/JGepM.webp)"
			if hosts := server.unmanagedArticleImageHosts(renderMarkdown(markdown).HTML); len(hosts) != 0 {
				t.Fatalf("existing managed image blocks text edit: %v", hosts)
			}
		})
	}
}

func TestAttachmentStorageBaseBoundaries(t *testing.T) {
	cases := []struct {
		name, base, source string
		allowed            bool
	}{
		{"root", "https://assets.example", "https://assets.example/uploads/a.png", true},
		{"root slash", "https://assets.example/", "https://assets.example/uploads/a.png", true},
		{"prefix", "https://assets.example/uploads", "https://assets.example/uploads/a.png", true},
		{"sibling prefix", "https://assets.example/uploads", "https://assets.example/uploads-evil/a.png", false},
		{"traversal", "https://assets.example/uploads", "https://assets.example/uploads/../private/a.png", false},
		{"other host", "https://assets.example", "https://evil.example/uploads/a.png", false},
		{"host suffix", "https://assets.example", "https://assets.example.evil.test/a.png", false},
		{"http downgrade", "https://assets.example", "http://assets.example/a.png", false},
		{"local", "", "/api/uploads/a.png", true},
		{"other relative", "", "/private/a.png", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedAttachmentURL(tc.source, tc.base); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestProtectedArticleRejectsPublicObjectURLAliases(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicSiteURL: "https://reader.example", PublicUploadBaseURL: "/api/uploads", R2PublicBaseURL: "https://cdn.example/uploads"}}
	r := httptest.NewRequest("PUT", "https://admin.example/api/admin/posts/qa", nil)
	for _, raw := range []string{
		"/api/uploads/qa.txt", "/api/uploads/qa.txt?download=1", "/api/uploads/qa.txt#download",
		"https://READER.example:443/api/uploads//qa.txt", "/api/%75ploads/qa.txt", "/api/uploads/x/../qa.txt",
		"https://CDN.example:443/uploads/qa.txt?download=1", "https://cdn.example/uploads/%71a.txt", "//cdn.example/uploads/qa.txt",
	} {
		if !s.hasPublicArticleMedia(r, `<a href="`+raw+`">file</a>`) {
			t.Errorf("public alias accepted: %s", raw)
		}
	}
	if s.hasPublicArticleMedia(r, `<a href="https://example.org/article">normal link</a>`) {
		t.Fatal("ordinary external link was blocked")
	}
}

func TestManagedArticleNormalizesSameOriginAbsoluteDestinations(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicSiteURL: "https://reader.example"}}
	r := httptest.NewRequest("PUT", "https://admin.example/api/admin/posts/qa", nil)
	path := "/api/post-media/00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-000000000002"
	for _, raw := range []string{"https://reader.example" + path, "//reader.example" + path, "https://ADMIN.example:443" + path, "https://reader.example" + path + "?download=1"} {
		markdown := "[附件: QA](" + raw + ")"
		if got := s.normalizeManagedArticleReferences(r, markdown, `<a href="`+raw+`">QA</a>`); got != "[附件: QA]("+path+")" {
			t.Errorf("not normalized: %s", got)
		}
		html := `<img src="` + raw + `">`
		if got := s.normalizeManagedArticleReferences(r, html, html); got != `<img src="`+path+`">` {
			t.Errorf("HTML source not normalized: %s", got)
		}
	}
	other := "[附件: QA](https://external.example" + path + ")"
	if got := s.normalizeManagedArticleReferences(r, other, renderMarkdown(other).HTML); got != other {
		t.Fatal("rewrote another site's URL")
	}
}
