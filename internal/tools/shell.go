// shell.go 实现 shell_tool / grep_search / glob_search 三个工具。
//
// 对应 Python 版 src/tools/shell/ 与 src/tools/search/。这里是精简实现：
//   - shell_tool 直接通过 sh -c 执行命令（含基础危险命令拦截）
//   - grep_search 用 Go 正则遍历文件搜索内容（不依赖外部 ripgrep）
//   - glob_search 用 filepath.Glob/WalkDir 做文件名匹配
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---- shell_tool ----

type ShellTool struct{}

func (ShellTool) Name() string        { return "shell_tool" }
func (ShellTool) Description() string { return "在 shell 中执行命令并返回标准输出/错误。默认超时 60 秒。" }
func (ShellTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "要执行的 shell 命令"},
			"cwd":     map[string]any{"type": "string", "description": "可选，执行命令的工作目录"},
		},
		"required": []string{"command"},
	}
}

// dangerousPatterns 是最基础的危险命令拦截（对应 Python 版 security.py 的极简子集）。
var dangerousPatterns = []string{
	"rm -rf /", "rm -rf /*", "mkfs", ":(){:|:&};:", "> /dev/sda", "dd if=",
}

func (ShellTool) Execute(args map[string]any) (string, error) {
	command := strArg(args, "command")
	if command == "" {
		return "", fmt.Errorf("缺少参数 command")
	}
	lc := strings.ToLower(command)
	for _, p := range dangerousPatterns {
		if strings.Contains(lc, p) {
			return "", fmt.Errorf("命令被安全策略拦截: 包含危险模式 %q", p)
		}
	}

	cmd := exec.Command("sh", "-c", command)
	if cwd := strArg(args, "cwd"); cwd != "" {
		cmd.Dir = cwd
	}

	// 超时控制。
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		return string(out) + "\n[超时] 命令执行超过 60 秒已被终止。", nil
	}

	result := string(out)
	if runErr != nil {
		result += fmt.Sprintf("\n[退出错误] %v", runErr)
	}
	if strings.TrimSpace(result) == "" {
		result = "(命令执行成功，无输出)"
	}
	return result, nil
}

// ---- grep_search ----

type GrepSearch struct{}

func (GrepSearch) Name() string { return "grep_search" }
func (GrepSearch) Description() string {
	return "在指定目录下递归搜索匹配正则的文件内容，返回 文件:行号:内容。"
}
func (GrepSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "要搜索的正则表达式"},
			"path":    map[string]any{"type": "string", "description": "搜索根目录，默认当前目录"},
		},
		"required": []string{"pattern"},
	}
}
func (GrepSearch) Execute(args map[string]any) (string, error) {
	pattern := strArg(args, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("缺少参数 pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("正则编译失败: %w", err)
	}
	root := strArg(args, "path")
	if root == "" {
		root = "."
	}

	var matches []string
	const maxMatches = 200
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// 跳过常见无关目录。
		if strings.Contains(p, "/.git/") || strings.Contains(p, "/node_modules/") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", p, i+1, strings.TrimSpace(line)))
				if len(matches) >= maxMatches {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "未找到匹配项。", nil
	}
	return strings.Join(matches, "\n"), nil
}

// ---- glob_search ----

type GlobSearch struct{}

func (GlobSearch) Name() string { return "glob_search" }
func (GlobSearch) Description() string {
	return "按 glob 模式（如 **/*.go）递归匹配文件名，返回文件路径列表。"
}
func (GlobSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "glob 模式，如 *.go 或 **/*.yaml"},
			"path":    map[string]any{"type": "string", "description": "搜索根目录，默认当前目录"},
		},
		"required": []string{"pattern"},
	}
}
func (GlobSearch) Execute(args map[string]any) (string, error) {
	pattern := strArg(args, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("缺少参数 pattern")
	}
	root := strArg(args, "path")
	if root == "" {
		root = "."
	}

	// 支持 ** 递归：用 WalkDir + filepath.Match 对 basename 或相对路径匹配。
	base := strings.TrimPrefix(pattern, "**/")
	var results []string
	const maxResults = 300
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(p, "/.git/") || strings.Contains(p, "/node_modules/") {
			return nil
		}
		name := filepath.Base(p)
		if ok, _ := filepath.Match(base, name); ok {
			results = append(results, p)
			if len(results) >= maxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "未匹配到文件。", nil
	}
	return strings.Join(results, "\n"), nil
}
