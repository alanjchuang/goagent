// Package tools 定义工具接口、注册表与内置工具。
//
// 对应 Python 版 src/lib/smolagents/tools/ 与 src/tools/。每个工具实现 Tool 接口，
// 通过 Schema() 暴露给 LLM，通过 Execute() 实际执行。
package tools

import (
	"encoding/json"
	"fmt"

	"github.com/alanjchuang/goagent/internal/llm"
)

// Tool 是所有工具的统一接口。
type Tool interface {
	// Name 返回工具的规范名称（LLM 调用时使用）。
	Name() string
	// Description 返回给 LLM 的工具说明。
	Description() string
	// Parameters 返回 JSON Schema 形式的参数定义（OpenAI function parameters）。
	Parameters() map[string]any
	// Execute 执行工具，args 为已解析的参数，返回字符串结果。
	Execute(args map[string]any) (string, error)
}

// Registry 是工具注册表。
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册一个工具。
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get 按名称取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Schemas 返回所有已注册工具的 LLM schema。
func (r *Registry) Schemas() []llm.ToolSchema {
	schemas := make([]llm.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		schemas = append(schemas, llm.ToolSchema{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return schemas
}

// Execute 解析 JSON 参数并调用对应工具。
func (r *Registry) Execute(name, argsJSON string) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("未知工具: %s", name)
	}
	args := map[string]any{}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("解析工具参数失败: %w; raw=%s", err, argsJSON)
		}
	}
	return t.Execute(args)
}

// strArg 是从 args 中安全取字符串参数的辅助函数。
func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}
