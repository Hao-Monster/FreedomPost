package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCommentSanitizer_StripsTags(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"script tag stripped", "hello <script>alert(1)</script> world", "hello  world"},
		{"img onerror stripped", `<img src="x" onerror="alert(1)">`, ""},
		{"bold tag stripped", "<b>粗体文字</b>", "粗体文字"},
		{"anchor stripped text kept", `<a href="https://evil.com">click</a>`, "click"},
		{"plain markdown preserved", "**bold** and `code`", "**bold** and `code`"},
		{"plain chinese unchanged", "这是普通中文评论。", "这是普通中文评论。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commentSanitizer.Sanitize(tc.input)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMaxCommentRunes_Limit(t *testing.T) {
	exactly := strings.Repeat("A", maxCommentRunes)
	if utf8.RuneCountInString(exactly) != maxCommentRunes {
		t.Fatalf("setup error")
	}
	over := strings.Repeat("A", maxCommentRunes+1)
	if utf8.RuneCountInString(over) <= maxCommentRunes {
		t.Errorf("over-limit string should have more than %d runes", maxCommentRunes)
	}
}

func TestIsAllowedAttachmentURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		bases   []string
		allowed bool
	}{
		{"empty URL allowed", "", nil, true},
		{"local upload path allowed", "/api/uploads/uuid.jpg", nil, true},
		{"OSS URL allowed", "https://bucket.oss.aliyuncs.com/fp/file.jpg", []string{"https://bucket.oss.aliyuncs.com/fp"}, true},
		{"R2 URL allowed", "https://pub.r2.dev/fp/file.jpg", []string{"https://pub.r2.dev/fp"}, true},
		{"evil URL rejected", "https://evil.com/x", []string{"/api/uploads"}, false},
		{"javascript scheme rejected", "javascript:alert(1)", []string{"/api/uploads"}, false},
		{"data URI rejected", "data:text/html,<h1>", []string{"/api/uploads"}, false},
		{"empty base does not match", "https://anything.com/file.jpg", []string{""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAllowedAttachmentURL(tc.url, tc.bases...)
			if got != tc.allowed {
				t.Errorf("isAllowedAttachmentURL(%q, %v) = %v, want %v", tc.url, tc.bases, got, tc.allowed)
			}
		})
	}
}
