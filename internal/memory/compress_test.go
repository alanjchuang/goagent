package memory

import (
	"strings"
	"testing"

	"github.com/alanjchuang/goagent/internal/llm"
)

func TestTruncateToolOutputs(t *testing.T) {
	long := strings.Repeat("x", 10000)
	msgs := []llm.Message{
		{Role: llm.RoleTool, Name: "shell_tool", Content: long},
	}
	out := Compress(msgs, Config{MaxToolOutputChars: 1000})
	if len(out[0].Content) >= 10000 {
		t.Errorf("工具输出未被截断, 长度=%d", len(out[0].Content))
	}
	if !strings.Contains(out[0].Content, "省略") {
		t.Error("截断结果应包含省略标记")
	}
}

func TestDedupFileReads(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleTool, Name: "read_file", Content: "FILE CONTENT A"},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleTool, Name: "read_file", Content: "FILE CONTENT A"},
	}
	out := Compress(msgs, Config{})
	if out[0].Content == "FILE CONTENT A" {
		t.Error("较早的重复 read_file 应被替换为占位符")
	}
	if out[2].Content != "FILE CONTENT A" {
		t.Error("最后一次 read_file 应保留原内容")
	}
}

func TestSlidingWindow(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "sys"}}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "task"})
	// 加入大量中间消息
	for i := 0; i < 20; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("y", 2000)})
	}
	out := Compress(msgs, Config{MaxTotalChars: 8000})
	if len(out) >= len(msgs) {
		t.Errorf("滑窗未生效: %d -> %d", len(msgs), len(out))
	}
	// 系统消息必须保留
	if out[0].Role != llm.RoleSystem {
		t.Error("系统消息应被保留在首位")
	}
}
