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
