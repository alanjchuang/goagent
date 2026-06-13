// codeact.go 实现 code_act 执行模式。
//
// 对应 AgentLoom 的 code_act 模式。与结构化 tool_call 不同，code_act 让模型
// 直接输出代码块（python 或 bash），框架提取并执行，把 stdout/stderr 回填到
// 对话继续循环，直到模型输出 final_answer(...) 调用。
//
// 当 agent YAML 的 tool_call_type 为 "code_act" 时，Run 走本模式。
package agent

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/checkpoint"
	"github.com/alanjchuang/goagent/internal/hooks"
	"github.com/alanjchuang/goagent/internal/llm"
	"github.com/alanjchuang/goagent/internal/logging"
	"github.com/alanjchuang/goagent/internal/memory"
	"github.com/alanjchuang/goagent/internal/tools"
)

// codeBlockRe 匹配 ```python / ```bash / ```sh 代码块。
var codeActBlockRe = regexp.MustCompile("(?s)```(python|py|bash|sh)\\s*\\n(.*?)```")

// finalAnswerCallRe 匹配 final_answer("...") 或 final_answer('...') 调用。
var finalAnswerCallRe = regexp.MustCompile(`(?s)final_answer\s*\(\s*["'](.+?)["']\s*\)`)

// buildCodeActPrompt 构造 code_act 模式的系统提示词。
func (a *Agent) buildCodeActPrompt() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("你是一个名为 %q 的 AI Agent，工作在 code_act 模式。\n\n", a.cfg.Name))
	sb.WriteString("## 你的职责\n")
	sb.WriteString(a.cfg.Description)
	sb.WriteString("\n\n## 工作流程\n")
	sb.WriteString(a.cfg.Workflow)
	sb.WriteString("\n\n## 执行方式（重要）\n")
	sb.WriteString("- 你通过编写并执行代码来完成任务，而不是调用结构化工具。\n")
	sb.WriteString("- 每一步输出一个代码块（```python 或 ```bash），系统会执行它并把输出返回给你。\n")
	sb.WriteString("- 根据执行结果决定下一步，逐步推进。\n")
	sb.WriteString("- 完成任务时，输出 `final_answer(\"你的最终答复\")` 来结束（写在代码块外或代码块内均可）。\n")
	sb.WriteString("- 每次只输出一个代码块。\n")
	return sb.String()
}

// runCodeAct 执行 code_act 模式的主循环。
func (a *Agent) runCodeAct(ctx context.Context, task string) (string, error) {
	a.hooks.Fire(hooks.TaskStart, hooks.Context{
		Event: string(hooks.TaskStart), AgentName: a.cfg.Name, Task: task,
	})
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: a.buildCodeActPrompt()},
		{Role: llm.RoleUser, Content: task},
	}
	if len(a.resumeMsgs) > 0 {
		messages = a.resumeMsgs
		logging.Get().Info("从 checkpoint 恢复(code_act): %d 条历史消息", len(messages))
	}

	for step := 1; step <= maxSteps; step++ {
		fmt.Printf("\n──────── Step %d (code_act) ────────\n", step)
		logging.Get().Info("Step %d 开始 [code_act]", step)

		sendMsgs := memory.Compress(messages, memory.DefaultConfig())
		// code_act 模式不发送 tool schema。
		resp, err := a.client.Complete(ctx, sendMsgs, nil)
		if err != nil {
			return "", err
		}
		messages = append(messages, *resp)
		a.saveCheckpoint(messages, checkpoint.StatusRunning, "")

		content := resp.Content
		fmt.Printf("[assistant]\n%s\n", content)

		// 1) 检查是否给出 final_answer(...)。
		if m := finalAnswerCallRe.FindStringSubmatch(content); m != nil {
			ans := m[1]
			fmt.Printf("[final_answer] %s\n", ans)
			a.fireComplete(ans)
			return ans, nil
		}

		// 2) 提取并执行代码块。
		blocks := codeActBlockRe.FindAllStringSubmatch(content, -1)
		if len(blocks) == 0 {
			// 没有代码块也没有 final_answer：把文本当作答复返回。
			if strings.TrimSpace(content) != "" {
				a.fireComplete(content)
				return content, nil
			}
			messages = append(messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "请输出一个代码块来推进任务，或用 final_answer(\"...\") 结束。",
			})
			continue
		}

		lang := blocks[0][1]
		code := blocks[0][2]
		a.hooks.Fire(hooks.PreToolUse, hooks.Context{
			Event: string(hooks.PreToolUse), ToolName: "code_act:" + lang, ToolArgs: code, AgentName: a.cfg.Name,
		})
		output := a.execCode(lang, code)
		a.hooks.Fire(hooks.PostToolUse, hooks.Context{
			Event: string(hooks.PostToolUse), ToolName: "code_act:" + lang, ToolResult: output, AgentName: a.cfg.Name,
		})
		printToolResult(output)

		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "代码执行输出:\n" + output,
		})
	}
	return "", fmt.Errorf("达到最大步数 %d 仍未完成任务", maxSteps)
}

// execCode 执行一段代码（python 或 bash），带安全检查与超时，返回合并输出。
func (a *Agent) execCode(lang, code string) string {
	var cmd *exec.Cmd
	switch lang {
	case "python", "py":
		cmd = exec.Command("python3", "-c", code)
	default: // bash / sh
		// 复用 shell 安全策略对 bash 代码做基础校验。
		if err := tools.CheckCommandWithActive(code); err != nil {
			return "代码被安全策略拦截: " + err.Error()
		}
		cmd = exec.Command("sh", "-c", code)
	}

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
		return string(out) + "\n[超时] 代码执行超过 60 秒已被终止。"
	}

	result := string(out)
	if runErr != nil {
		result += fmt.Sprintf("\n[退出错误] %v", runErr)
	}
	if strings.TrimSpace(result) == "" {
		result = "(代码执行成功，无输出)"
	}
	return result
}
