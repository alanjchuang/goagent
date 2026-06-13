// edit_file.go 实现 edit_file 工具：基于精确字符串替换编辑文件。
//
// 对应 Python 版 src/tools/file_ops/edit_file/。给定 old_string 与 new_string，
// 在文件中查找 old_string 并替换为 new_string。要求 old_string 唯一匹配，
// 否则报错以避免误改（与原版的安全语义一致）。
package tools

import (
	"fmt"
	"os"
	"strings"
)

type EditFile struct{}

func (EditFile) Name() string { return "edit_file" }
func (EditFile) Description() string {
	return "编辑文件：把文件中的 old_string 精确替换为 new_string。" +
		"old_string 必须在文件中唯一出现（否则报错）。用于对已有文件做局部修改。"
}
func (EditFile) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "要编辑的文件路径"},
			"old_string":  map[string]any{"type": "string", "description": "要被替换的原文本（需在文件中唯一出现）"},
			"new_string":  map[string]any{"type": "string", "description": "替换后的新文本"},
			"replace_all": map[string]any{"type": "boolean", "description": "是否替换所有匹配，默认 false"},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}
func (EditFile) Execute(args map[string]any) (string, error) {
	path := strArg(args, "file_path")
	oldStr := strArg(args, "old_string")
	newStr := strArg(args, "new_string")
	if path == "" {
		return "", fmt.Errorf("缺少参数 file_path")
	}
	if oldStr == "" {
		return "", fmt.Errorf("缺少参数 old_string")
	}
	if err := activePolicy.CheckPath(path); err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)

	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("在文件 %s 中未找到 old_string", path)
	}

	replaceAll := false
	if v, ok := args["replace_all"].(bool); ok {
		replaceAll = v
	}

	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		if count > 1 {
			return "", fmt.Errorf("old_string 在文件 %s 中出现 %d 次，不唯一；请提供更多上下文使其唯一，或设置 replace_all=true", path, count)
		}
		newContent = strings.Replace(content, oldStr, newStr, 1)
	}

	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("已编辑 %s（替换 %d 处）", path, count), nil
}
