// builtin.go 实现基础内置工具：read_file / write_file / browse_directory /
// get_file_outline / final_answer。对应 Python 版 src/tools/file_ops/。
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- read_file ----

type ReadFile struct{}

func (ReadFile) Name() string        { return "read_file" }
func (ReadFile) Description() string { return "读取指定路径文件的全部文本内容。" }
func (ReadFile) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "要读取的文件路径"},
		},
		"required": []string{"file_path"},
	}
}
func (ReadFile) Execute(args map[string]any) (string, error) {
	path := strArg(args, "file_path")
	if path == "" {
		return "", fmt.Errorf("缺少参数 file_path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ---- write_file ----

type WriteFile struct{}

func (WriteFile) Name() string        { return "write_file" }
func (WriteFile) Description() string { return "将内容写入指定路径文件（覆盖写）。自动创建父目录。" }
func (WriteFile) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "目标文件路径"},
			"content":   map[string]any{"type": "string", "description": "要写入的内容"},
		},
		"required": []string{"file_path", "content"},
	}
}
func (WriteFile) Execute(args map[string]any) (string, error) {
	path := strArg(args, "file_path")
	content := strArg(args, "content")
	if path == "" {
		return "", fmt.Errorf("缺少参数 file_path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("已写入 %d 字节到 %s", len(content), path), nil
}

// ---- browse_directory ----

type BrowseDirectory struct{}

func (BrowseDirectory) Name() string        { return "browse_directory" }
func (BrowseDirectory) Description() string { return "列出指定目录下的文件和子目录。" }
func (BrowseDirectory) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "要浏览的目录路径，默认当前目录"},
		},
	}
}
func (BrowseDirectory) Execute(args map[string]any) (string, error) {
	path := strArg(args, "path")
	if path == "" {
		path = "."
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("目录 %s 内容:\n", path))
	for _, e := range entries {
		marker := "  "
		if e.IsDir() {
			marker = "📁"
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", marker, e.Name()))
	}
	return sb.String(), nil
}

// ---- get_file_outline ----

type GetFileOutline struct{}

func (GetFileOutline) Name() string { return "get_file_outline" }
func (GetFileOutline) Description() string {
	return "返回文件的大纲：行数与前若干行预览，用于快速了解文件结构。"
}
func (GetFileOutline) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "目标文件路径"},
		},
		"required": []string{"file_path"},
	}
}
func (GetFileOutline) Execute(args map[string]any) (string, error) {
	path := strArg(args, "file_path")
	if path == "" {
		return "", fmt.Errorf("缺少参数 file_path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("文件 %s 共 %d 行。预览前 40 行:\n", path, len(lines)))
	for i, line := range lines {
		if i >= 40 {
			sb.WriteString("...\n")
			break
		}
		sb.WriteString(fmt.Sprintf("%4d| %s\n", i+1, line))
	}
	return sb.String(), nil
}

// RegisterBuiltins 把内置工具按名称注册到 registry。
// names 为 agent YAML 声明的工具名列表；未知名称会被忽略。
func RegisterBuiltins(r *Registry, names []string) {
	all := map[string]Tool{
		"read_file":        ReadFile{},
		"write_file":       WriteFile{},
		"browse_directory": BrowseDirectory{},
		"get_file_outline": GetFileOutline{},
		"shell_tool":       ShellTool{},
		"grep_search":      GrepSearch{},
		"glob_search":      GlobSearch{},
		"edit_file":        EditFile{},
		"web_search":       WebSearch{},
	}
	for _, n := range names {
		if t, ok := all[n]; ok {
			r.Register(t)
		}
	}
}
