package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWeChatCreateDraftDryRun(t *testing.T) {
	out, err := (WeChatCreateDraft{}).Execute(map[string]any{
		"title":   "测试标题",
		"author":  "测试作者",
		"digest":  "测试摘要",
		"content": "<![CDATA[\n<p>测试正文</p>\n]]>",
		"dry_run": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "draft_payload") || !strings.Contains(out, "missing_before_real_call") {
		t.Fatalf("unexpected dry-run output: %s", out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if result["dry_run"] != true {
		t.Fatalf("expected dry_run=true, got %v", result["dry_run"])
	}
	payload := result["draft_payload"].(map[string]any)
	articles := payload["articles"].([]any)
	article := articles[0].(map[string]any)
	if got := article["content"]; got != "<p>测试正文</p>" {
		t.Fatalf("CDATA should be stripped, got %q", got)
	}
}

func TestSanitizeWeChatHTMLContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain html",
			in:   `<section><p>hello</p></section>`,
			want: `<section><p>hello</p></section>`,
		},
		{
			name: "cdata html",
			in:   `<![CDATA[ <section><p>hello</p></section> ]]>`,
			want: `<section><p>hello</p></section>`,
		},
		{
			name: "spaced cdata html",
			in:   `< ![CDATA[ <section><p>hello</p></section> ]]>`,
			want: `<section><p>hello</p></section>`,
		},
		{
			name: "markdown html fence",
			in:   "```html\n<section><p>hello</p></section>\n```",
			want: `<section><p>hello</p></section>`,
		},
		{
			name: "markdown fenced cdata",
			in:   "```html\n<![CDATA[\n<section><p>hello</p></section>\n]]>\n```",
			want: `<section><p>hello</p></section>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeWeChatHTMLContent(tt.in); got != tt.want {
				t.Fatalf("sanitizeWeChatHTMLContent()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceLocalImagesForWeChatSkipsRemoteAndPlaceholders(t *testing.T) {
	content := `<p><img src="https://example.com/a.jpg"><img src="{{image_1}}"></p>`
	out, replacements, err := replaceLocalImagesForWeChat(nil, "", "", content)
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Fatalf("content should not change: %s", out)
	}
	if len(replacements) != 0 {
		t.Fatalf("unexpected replacements: %+v", replacements)
	}
}
