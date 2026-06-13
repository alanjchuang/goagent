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
		"content": "<p>测试正文</p>",
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
