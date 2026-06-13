// final_answer.go 提供 final_answer 工具及参数解析。
// 对应 Python 版 smolagents 的 FinalAnswerTool。
package agent

import (
	"encoding/json"

	"github.com/alanjchuang/goagent/internal/tools"
)

// finalAnswerTool 让模型显式结束任务并给出答复。
// 它的 Execute 实际不会被注册表调用（agent 循环会拦截 final_answer），
// 这里实现接口仅用于把 schema 暴露给 LLM。
type finalAnswerTool struct{}

func (*finalAnswerTool) Name() string        { return "final_answer" }
func (*finalAnswerTool) Description() string { return "提供任务的最终答复并结束运行。" }
func (*finalAnswerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string", "description": "给用户的最终答复"},
		},
		"required": []string{"answer"},
	}
}
func (*finalAnswerTool) Execute(args map[string]any) (string, error) {
	if v, ok := args["answer"].(string); ok {
		return v, nil
	}
	return "", nil
}

var _ tools.Tool = (*finalAnswerTool)(nil)

// extractFinalAnswer 从 final_answer 调用的 JSON 参数中取出 answer 字段。
// 容错：若不是合法 JSON，则原样返回。
func extractFinalAnswer(argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err == nil {
		if v, ok := m["answer"].(string); ok {
			return v
		}
	}
	return argsJSON
}
