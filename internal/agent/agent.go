// Package agent 实现 ToolCalling Agent 的主循环。
//
// 对应 Python 版 src/lib/smolagents/agent/。原版基于 smolagents 的 ToolCallingAgent，
// 这里用一个简化但完整的 think-act 循环：构建 system prompt → 调用 LLM →
// 执行工具 → 把结果回填到对话 → 循环，直到模型调用 final_answer 或无更多工具调用。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alanjchuang/goagent/internal/checkpoint"
	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/events"
	"github.com/alanjchuang/goagent/internal/hooks"
	"github.com/alanjchuang/goagent/internal/llm"
	"github.com/alanjchuang/goagent/internal/logging"
	"github.com/alanjchuang/goagent/internal/mcp"
	"github.com/alanjchuang/goagent/internal/memory"
	"github.com/alanjchuang/goagent/internal/skills"
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
	skills   *skills.Registry
	hooks    *hooks.Manager

	// checkpoint 相关（可选）。设置后每步保存状态，支持断点恢复。
	ckpt        *checkpoint.Manager
	ckptState   *checkpoint.State
	resumeMsgs  []llm.Message // 恢复时预填的历史消息

	mcpClients []*mcp.Client // 已连接的 MCP server，需在结束时关闭
}

// Close 释放 agent 持有的资源（如 MCP 连接）。
func (a *Agent) Close() {
	closeMCPClients(a.mcpClients)
}

// EnableCheckpoint 开启 checkpoint：每步持久化状态到 mgr，使用给定 state。
// 若 state.Messages 非空，则作为恢复历史继续执行。
func (a *Agent) EnableCheckpoint(mgr *checkpoint.Manager, state *checkpoint.State) {
	a.ckpt = mgr
	a.ckptState = state
	if len(state.Messages) > 0 {
		a.resumeMsgs = state.Messages
	}
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

	// 加载 skills（来自 agent YAML 声明的 skills 目录）。
	skReg := skills.NewRegistry()
	for _, s := range cfg.Skills {
		if s.Path == "" {
			continue
		}
		dir := s.Path
		if !filepath.IsAbs(dir) && config.C != nil {
			dir = filepath.Join(config.C.AgentRoot, s.Path)
		}
		if err := skReg.LoadDir(dir); err != nil {
			return nil, err
		}
	}
	// 若存在可见 skill，则注册 load_skill / list_skills 工具。
	if len(skReg.Listable()) > 0 {
		reg.Register(&skills.ListSkillsTool{Reg: skReg})
		reg.Register(&skills.LoadSkillTool{Reg: skReg})
	}

	// 从所有 skill 的 frontmatter 注册生命周期 hook。
	hookMgr := hooks.NewManager()
	for _, s := range skReg.All() {
		if len(s.Hooks) > 0 {
			hookMgr.RegisterFromSkill(s.Hooks, s.Dir, s.Name)
		}
	}

	// 连接 MCP server 并注册其工具（若配置了 mcp_servers）。
	mcpClients := registerMCPTools(reg)

	// 始终注册 final_answer，供模型结束任务。
	reg.Register(&finalAnswerTool{})

	return &Agent{cfg: cfg, client: client, registry: reg, skills: skReg, hooks: hookMgr, mcpClients: mcpClients}, nil
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

	// 列出可用工具，并说明文本调用格式。这对不支持原生 tool_call 的模型很关键：
	// 它们看不到 tools 字段，只能靠系统提示词知道有哪些工具、如何调用。
	sb.WriteString("\n## 可用工具\n")
	for _, s := range a.registry.Schemas() {
		params, _ := json.Marshal(s.Function.Parameters)
		sb.WriteString(fmt.Sprintf("- %s: %s\n  参数 schema: %s\n",
			s.Function.Name, s.Function.Description, string(params)))
	}
	sb.WriteString("\n## 文本调用格式（当你无法使用原生工具调用时）\n")
	sb.WriteString("用如下 JSON（可放在 ```json 代码块中）发起一次工具调用:\n")
	sb.WriteString("{\"name\": \"工具名\", \"arguments\": {\"参数名\": \"参数值\"}}\n")
	sb.WriteString("每次只调用一个工具。\n")

	// 强制注入模式的 skill：正文直接拼进系统提示词。
	if a.skills != nil {
		for _, s := range a.skills.ForceInjected() {
			sb.WriteString(fmt.Sprintf("\n## Skill: %s\n%s\n", s.Name, s.Body))
		}
	}
	return sb.String()
}

// Run 执行 agent，task 为任务文本，返回最终答复。
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	// code_act 模式：让模型写并执行代码，而非结构化工具调用。
	if a.cfg.ToolCallType == "code_act" {
		return a.runCodeAct(ctx, task)
	}
	a.hooks.Fire(hooks.TaskStart, hooks.Context{
		Event: string(hooks.TaskStart), AgentName: a.cfg.Name, Task: task,
	})
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: a.buildSystemPrompt()},
		{Role: llm.RoleUser, Content: task},
	}
	// 断点恢复：若有持久化的历史消息，则在其基础上继续。
	if len(a.resumeMsgs) > 0 {
		messages = a.resumeMsgs
		logging.Get().Info("从 checkpoint 恢复: %d 条历史消息", len(messages))
	}
	schemas := a.registry.Schemas()

	for step := 1; step <= maxSteps; step++ {
		fmt.Printf("\n──────── Step %d ────────\n", step)
		logging.Get().Info("Step %d 开始 [tool_call 模式: %s]", step, a.client.NativeMode())
		events.Publish(events.Event{Type: "step", AgentName: a.cfg.Name, Step: step})
		// 三态检测：决定本次是否携带 tool schema（原生 tool_call）还是靠文本解析。
		var sentSchemas []llm.ToolSchema
		if a.client.UseNativeToolCalls() {
			sentSchemas = schemas
		}
		// context 压缩：在发送前对历史做去重/截断/滑窗，控制 token 预算。
		sendMsgs := memory.Compress(messages, memory.DefaultConfig())
		if len(sendMsgs) != len(messages) {
			logging.Get().Info("context 压缩: %s", memory.Summary(messages, sendMsgs))
		}
		resp, err := a.client.Complete(ctx, sendMsgs, sentSchemas)
		if err != nil {
			return "", err
		}
		// auto 模式：根据响应是否含原生 tool_calls 更新检测状态。
		a.client.UpdateNativeDetection(resp)
		messages = append(messages, *resp)
		a.saveCheckpoint(messages, checkpoint.StatusRunning, "")

		// 无原生 tool_calls：先尝试从文本内容中解析工具调用（兼容不支持原生
		// tool_call 的模型），解析不到再把文本当作最终答复。
		if len(resp.ToolCalls) == 0 {
			if call, ok := toolparse.Parse(resp.Content, a.knownTools()); ok {
				fmt.Printf("[text-parsed tool_call:%s] %s(%s)\n", call.Strategy, call.Name, call.Arguments)
				if call.Name == "final_answer" {
					ans := extractFinalAnswer(call.Arguments)
					fmt.Printf("[final_answer] %s\n", ans)
					a.fireComplete(ans)
					return ans, nil
				}
				result := a.execTool(call.Name, call.Arguments)
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
				a.fireComplete(resp.Content)
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
				a.fireComplete(ans)
				return ans, nil
			}

			result := a.execTool(tc.Function.Name, tc.Function.Arguments)
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

// execTool 执行一个工具调用，并在前后触发 Pre/PostToolUse hook。
func (a *Agent) execTool(name, argsJSON string) string {
	a.hooks.Fire(hooks.PreToolUse, hooks.Context{
		Event: string(hooks.PreToolUse), ToolName: name, ToolArgs: argsJSON, AgentName: a.cfg.Name,
	})
	events.Publish(events.Event{Type: "tool_call", AgentName: a.cfg.Name, Detail: name + "(" + argsJSON + ")"})
	result, err := a.registry.Execute(name, argsJSON)
	if err != nil {
		result = "工具执行错误: " + err.Error()
	}
	events.Publish(events.Event{Type: "tool_result", AgentName: a.cfg.Name, Detail: truncateForEvent(result)})
	a.hooks.Fire(hooks.PostToolUse, hooks.Context{
		Event: string(hooks.PostToolUse), ToolName: name, ToolArgs: argsJSON,
		ToolResult: result, AgentName: a.cfg.Name,
	})
	return result
}

// truncateForEvent 截断事件详情，避免 UI 推送过长内容。
func truncateForEvent(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// fireComplete 触发 TaskComplete hook，并把 checkpoint 标记为已完成。
func (a *Agent) fireComplete(result string) {
	events.Publish(events.Event{Type: "final_answer", AgentName: a.cfg.Name, Detail: truncateForEvent(result)})
	a.hooks.Fire(hooks.TaskComplete, hooks.Context{
		Event: string(hooks.TaskComplete), AgentName: a.cfg.Name, ToolResult: result,
	})
	if a.ckpt != nil && a.ckptState != nil {
		a.ckptState.Status = checkpoint.StatusCompleted
		a.ckptState.Result = result
		if err := a.ckpt.Save(a.ckptState); err != nil {
			logging.Get().Warn("保存 checkpoint(completed) 失败: %v", err)
		}
	}
}

// saveCheckpoint 持久化当前对话与状态（若开启了 checkpoint）。
func (a *Agent) saveCheckpoint(messages []llm.Message, status checkpoint.Status, result string) {
	if a.ckpt == nil || a.ckptState == nil {
		return
	}
	a.ckptState.Messages = messages
	a.ckptState.Status = status
	if result != "" {
		a.ckptState.Result = result
	}
	if err := a.ckpt.Save(a.ckptState); err != nil {
		logging.Get().Warn("保存 checkpoint 失败: %v", err)
	}
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
