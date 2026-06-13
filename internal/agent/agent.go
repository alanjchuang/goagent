// Package agent 实现 ToolCalling Agent 的主循环。
//
// 对应 Python 版 src/lib/smolagents/agent/。原版基于 smolagents 的 ToolCallingAgent，
// 这里用一个简化但完整的 think-act 循环：构建 system prompt → 调用 LLM →
// 执行工具 → 把结果回填到对话 → 循环，直到模型调用 final_answer 或无更多工具调用。
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/llm"
	"github.com/alanjchuang/goagent/internal/toolparse"
	"github.com/alanjchuang/goagent/internal/tools"
)

// maxSteps 是单次运行的最大循环步数，防止死循环。
const maxSteps = 25

// Agent 是一个可运行的 agent 实例。
type Agent struct {
	cfg      *config.AgentConfig
	client   *llm.Client
	registry *tools.Registry
}

// New 根据 agent 配置构建 Agent，加载其工具与 LLM 客户端。
func New(cfg *config.AgentConfig) (*Agent, error) {
	client, err := llm.NewClient(cfg.ModelType)
	if err != nil {
		return nil, err
	}
	reg := tools.NewRegistry()

	// 收集 agent YAML 声明的工具名。
	names := make([]string, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		names = append(names, t.Name)
	}
	tools.RegisterBuiltins(reg, names)

	// 加载 worker_agents，把每个 worker 包装成可调用工具（Worker-as-Tool）。
	if err := registerWorkers(reg, cfg); err != nil {
		return nil, err
	}

	// 始终注册 final_answer，供模型结束任务。
	reg.Register(&finalAnswerTool{})

	return &Agent{cfg: cfg, client: client, registry: reg}, nil
}

// buildSystemPrompt 构造系统提示词（对应 Python 版 prompt_builder）。
func (a *Agent) buildSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("你是一个名为 %q 的 AI Agent。\n\n", a.cfg.Name))
	sb.WriteString("## 你的职责\n")
	sb.WriteString(a.cfg.Description)
	sb.WriteString("\n\n## 工作流程\n")
	sb.WriteString(a.cfg.Workflow)
	sb.WriteString("\n\n## 工具使用规则\n")
	sb.WriteString("- 你可以调用提供的工具来完成任务。\n")
	sb.WriteString("- 部分工具可能是子 Agent（worker），可把子任务委派给它们处理。\n")
	sb.WriteString("- 完成任务后，必须调用 final_answer 工具给出最终答复。\n")
	return sb.String()
}

// Run 执行 agent，task 为任务文本，返回最终答复。
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: a.buildSystemPrompt()},
		{Role: llm.RoleUser, Content: task},
	}
	schemas := a.registry.Schemas()

	for step := 1; step <= maxSteps; step++ {
		fmt.Printf("\n──────── Step %d ────────\n", step)
		resp, err := a.client.Complete(ctx, messages, schemas)
		if err != nil {
			return "", err
		}
		messages = append(messages, *resp)

		// 无原生 tool_calls：先尝试从文本内容中解析工具调用（兼容不支持原生
		// tool_call 的模型），解析不到再把文本当作最终答复。
		if len(resp.ToolCalls) == 0 {
			if call, ok := toolparse.Parse(resp.Content, a.knownTools()); ok {
				fmt.Printf("[text-parsed tool_call:%s] %s(%s)\n", call.Strategy, call.Name, call.Arguments)
				if call.Name == "final_answer" {
					ans := extractFinalAnswer(call.Arguments)
					fmt.Printf("[final_answer] %s\n", ans)
					return ans, nil
				}
				result, err := a.registry.Execute(call.Name, call.Arguments)
				if err != nil {
					result = "工具执行错误: " + err.Error()
				}
				printToolResult(result)
				// 文本解析出的工具调用没有 tool_call_id，回填为 user 消息。
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: fmt.Sprintf("工具 %s 的执行结果:\n%s", call.Name, result),
				})
				continue
			}
			if strings.TrimSpace(resp.Content) != "" {
				fmt.Printf("[assistant] %s\n", resp.Content)
				return resp.Content, nil
			}
			// 既无工具调用也无内容，提示模型继续。
			messages = append(messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "请继续：调用工具推进任务，或调用 final_answer 结束。",
			})
			continue
		}

		// 依次执行每个工具调用。
		for _, tc := range resp.ToolCalls {
			fmt.Printf("[tool_call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)

			if tc.Function.Name == "final_answer" {
				ans := extractFinalAnswer(tc.Function.Arguments)
				fmt.Printf("[final_answer] %s\n", ans)
				return ans, nil
			}

			result, err := a.registry.Execute(tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				result = "工具执行错误: " + err.Error()
			}
			printToolResult(result)

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}
	return "", fmt.Errorf("达到最大步数 %d 仍未完成任务", maxSteps)
}

// Usage 返回累计 token 用量。
func (a *Agent) Usage() llm.TokenUsage { return a.client.CumulativeUsage }

// knownTools 返回已注册工具名集合，供文本解析校验。
func (a *Agent) knownTools() map[string]bool { return a.registry.Names() }

// printToolResult 打印工具执行结果（超长时截断）。
func printToolResult(result string) {
	preview := result
	if len(preview) > 500 {
		preview = preview[:500] + "...(截断)"
	}
	fmt.Printf("[tool_result] %s\n", preview)
}
