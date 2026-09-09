package httpapi

import (
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
