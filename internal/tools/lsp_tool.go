// lsp_tool.go 提供基于 LSP 的代码智能工具：定义跳转、引用查找、文档符号。
//
// 对应 AgentLoom 的 lsp_find_definition / lsp_find_references /
// lsp_get_document_symbols。每次调用按需启动对应语言的 LSP server，查询后关闭。
// 需要本机已安装对应 LSP server（go: gopls；python: pyright；ts: typescript-language-server）。
package tools

import (
	"fmt"
	"path/filepath"

	"github.com/alanjchuang/goagent/internal/lsp"
)

// lspPositionParams 解析 file_path/line/character 三个公共参数。
func lspPositionParams(args map[string]any) (file string, line, char int, err error) {
	file = strArg(args, "file_path")
	if file == "" {
		return "", 0, 0, fmt.Errorf("缺少参数 file_path")
	}
	line = intArg(args, "line")
	char = intArg(args, "character")
	return file, line, char, nil
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func lspConnect(file string) (*lsp.Client, error) {
	root := "."
	if abs, err := filepath.Abs("."); err == nil {
		root = abs
	}
	return lsp.Connect(filepath.Ext(file), root)
}

// ---- lsp_find_definition ----

type LSPDefinition struct{}

func (LSPDefinition) Name() string { return "lsp_find_definition" }
func (LSPDefinition) Description() string {
	return "用 LSP 跳转到指定位置符号的定义。line/character 从 0 开始。"
}
func (LSPDefinition) Parameters() map[string]any {
	return lspPosParamSchema()
}
func (LSPDefinition) Execute(args map[string]any) (string, error) {
	file, line, char, err := lspPositionParams(args)
	if err != nil {
		return "", err
	}
	if err := activePolicy.CheckPath(file); err != nil {
		return "", err
	}
	c, err := lspConnect(file)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Definition(file, line, char)
}

// ---- lsp_find_references ----

type LSPReferences struct{}

func (LSPReferences) Name() string { return "lsp_find_references" }
func (LSPReferences) Description() string {
	return "用 LSP 查找指定位置符号的所有引用。line/character 从 0 开始。"
}
func (LSPReferences) Parameters() map[string]any {
	return lspPosParamSchema()
}
func (LSPReferences) Execute(args map[string]any) (string, error) {
	file, line, char, err := lspPositionParams(args)
	if err != nil {
		return "", err
	}
	if err := activePolicy.CheckPath(file); err != nil {
		return "", err
	}
	c, err := lspConnect(file)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.References(file, line, char)
}

// ---- lsp_get_document_symbols ----

type LSPDocumentSymbols struct{}

func (LSPDocumentSymbols) Name() string { return "lsp_get_document_symbols" }
func (LSPDocumentSymbols) Description() string {
	return "用 LSP 列出指定文件中的所有符号。"
}
func (LSPDocumentSymbols) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "目标文件路径"},
		},
		"required": []string{"file_path"},
	}
}
func (LSPDocumentSymbols) Execute(args map[string]any) (string, error) {
	file := strArg(args, "file_path")
	if file == "" {
		return "", fmt.Errorf("缺少参数 file_path")
	}
	if err := activePolicy.CheckPath(file); err != nil {
		return "", err
	}
	c, err := lspConnect(file)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.DocumentSymbols(file)
}

func lspPosParamSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "目标文件路径"},
			"line":      map[string]any{"type": "integer", "description": "行号(从 0 开始)"},
			"character": map[string]any{"type": "integer", "description": "列号(从 0 开始)"},
		},
		"required": []string{"file_path", "line", "character"},
	}
}
