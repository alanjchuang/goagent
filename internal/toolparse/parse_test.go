package toolparse

import "testing"

func known(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestParse(t *testing.T) {
	tools := known("read_file", "browse_directory", "final_answer")

	cases := []struct {
		name      string
		text      string
		wantTool  string
		wantStrat string
	}{
		{
			name:     "标准JSON",
			text:     `{"name": "read_file", "arguments": {"file_path": "a.go"}}`,
			wantTool: "read_file",
		},
		{
			name:     "代码块包裹JSON",
			text:     "好的，我来读取：\n```json\n{\"name\": \"read_file\", \"arguments\": {\"file_path\": \"a.go\"}}\n```",
			wantTool: "read_file",
		},
		{
			name:     "OpenAI嵌套function结构",
			text:     `{"function": {"name": "browse_directory", "arguments": "{\"path\": \".\"}"}}`,
			wantTool: "browse_directory",
		},
		{
			name:     "单引号需修复",
			text:     `{'name': 'read_file', 'arguments': {'file_path': 'a.go'}}`,
			wantTool: "read_file",
		},
		{
			name:     "XML tool_call标签",
			text:     `<tool_call>{"name": "read_file", "arguments": {"file_path": "a.go"}}</tool_call>`,
			wantTool: "read_file",
		},
		{
			name:     "invoke/parameter标签",
			text:     `<invoke name="read_file"><parameter name="file_path">a.go</parameter></invoke>`,
			wantTool: "read_file",
		},
		{
			name:     "ReAct风格Action",
			text:     "Thought: 我需要读文件\nAction: read_file\nAction Input: {\"file_path\": \"a.go\"}",
			wantTool: "read_file",
		},
		{
			name:     "函数调用风格",
			text:     `read_file({"file_path": "a.go"})`,
			wantTool: "read_file",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			call, ok := Parse(c.text, tools)
			if !ok {
				t.Fatalf("解析失败: %q", c.text)
			}
			if call.Name != c.wantTool {
				t.Errorf("工具名 = %q, 期望 %q (策略=%s)", call.Name, c.wantTool, call.Strategy)
			}
		})
	}
}

func TestParseNoMatch(t *testing.T) {
	tools := known("read_file")
	if _, ok := Parse("这只是一段普通的回复文本，没有工具调用。", tools); ok {
		t.Error("纯文本不应解析出工具调用")
	}
	// 未知工具名不应命中
	if _, ok := Parse(`{"name": "unknown_tool", "arguments": {}}`, tools); ok {
		t.Error("未知工具不应命中")
	}
}
