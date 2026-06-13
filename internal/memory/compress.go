// Package memory 实现对话历史的 context 压缩。
//
// 对应 AgentLoom src/lib/smolagents/memory/context_compression.py（精简版）。
// 在每次发送 LLM 请求前，对消息列表做多层压缩以控制 token 预算：
//   - Layer 1 文件读取去重：同一文件被多次 read_file，旧的结果替换为占位符
//   - Layer 2 工具输出截断：超长的工具结果保留头尾、中间省略
//   - Layer 3 滑窗截断：消息总量过大时，隐藏最旧的非系统消息
//
// 压缩是按字符数近似估算 token（约 1 token ≈ 4 字符），不依赖外部分词器。
package memory

import (
	"fmt"

	"github.com/alanjchuang/goagent/internal/llm"
)

// Config 控制压缩行为。
type Config struct {
	MaxToolOutputChars int // 单条工具结果保留的最大字符数；<=0 表示不截断
	MaxTotalChars      int // 整个对话的字符上限；超过则滑窗截断；<=0 表示不限制
}

// DefaultConfig 返回默认压缩配置。
func DefaultConfig() Config {
	return Config{
		MaxToolOutputChars: 4000,
		MaxTotalChars:      120000, // 约 30k token
	}
}

// Compress 对消息列表做多层压缩，返回压缩后的新列表（不修改入参）。
func Compress(messages []llm.Message, cfg Config) []llm.Message {
	out := make([]llm.Message, len(messages))
	copy(out, messages)

	out = dedupFileReads(out)
	if cfg.MaxToolOutputChars > 0 {
		out = truncateToolOutputs(out, cfg.MaxToolOutputChars)
	}
	if cfg.MaxTotalChars > 0 {
		out = slidingWindow(out, cfg.MaxTotalChars)
	}
	return out
}

// dedupFileReads 对 read_file 工具的重复结果去重：
// 同一文件路径被多次读取时，保留最后一次，较早的替换为占位符。
func dedupFileReads(messages []llm.Message) []llm.Message {
	// 找出每个 read_file 结果消息对应的"文件标识"。这里用结果内容本身去重：
	// 同样的工具名 read_file 且内容相同 → 视为重复读取。
	type key struct{ name, content string }
	lastIdx := map[key]int{}
	for i, m := range messages {
		if m.Role == llm.RoleTool && m.Name == "read_file" {
			k := key{m.Name, m.Content}
			lastIdx[k] = i
		}
	}
	for i := range messages {
		m := messages[i]
		if m.Role == llm.RoleTool && m.Name == "read_file" {
			k := key{m.Name, m.Content}
			if lastIdx[k] != i {
				messages[i].Content = "[此前已读取过相同内容，为节省上下文已省略。最新一次读取见后文。]"
			}
		}
	}
	return messages
}

// truncateToolOutputs 截断过长的工具结果，保留头尾。
func truncateToolOutputs(messages []llm.Message, maxChars int) []llm.Message {
	for i := range messages {
		if messages[i].Role != llm.RoleTool {
			continue
		}
		c := messages[i].Content
		if len(c) <= maxChars {
			continue
		}
		head := maxChars * 2 / 3
		tail := maxChars - head
		messages[i].Content = fmt.Sprintf("%s\n\n...[省略 %d 字符]...\n\n%s",
			c[:head], len(c)-maxChars, c[len(c)-tail:])
	}
	return messages
}

// slidingWindow 当总字符数超限时，从最旧的非系统消息开始隐藏，直到满足预算。
// 始终保留：第一条 system 消息、第一条 user 消息（任务）、以及最近的若干消息。
func slidingWindow(messages []llm.Message, maxChars int) []llm.Message {
	if totalChars(messages) <= maxChars {
		return messages
	}
	// 找到首个 system 和首个 user 的下标，作为保护区。
	keepHead := 0
	for i, m := range messages {
		if m.Role == llm.RoleUser {
			keepHead = i
			break
		}
	}
	// 从 keepHead+1 开始逐条标记隐藏，直到预算满足或只剩保护头+最近消息。
	hidden := make([]bool, len(messages))
	const keepRecent = 4 // 至少保留最近 4 条
	for i := keepHead + 1; i < len(messages)-keepRecent; i++ {
		if currentChars(messages, hidden) <= maxChars {
			break
		}
		hidden[i] = true
	}

	out := make([]llm.Message, 0, len(messages))
	skipped := 0
	for i, m := range messages {
		if hidden[i] {
			skipped++
			continue
		}
		out = append(out, m)
	}
	if skipped > 0 {
		// 在保护头之后插入一条压缩说明。
		note := llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("[上下文压缩] 为控制长度，已隐藏 %d 条较早的中间消息。", skipped),
		}
		insertAt := keepHead + 1
		if insertAt > len(out) {
			insertAt = len(out)
		}
		out = append(out[:insertAt], append([]llm.Message{note}, out[insertAt:]...)...)
	}
	return out
}

func msgChars(m llm.Message) int {
	n := len(m.Content)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

func totalChars(messages []llm.Message) int {
	n := 0
	for _, m := range messages {
		n += msgChars(m)
	}
	return n
}

func currentChars(messages []llm.Message, hidden []bool) int {
	n := 0
	for i, m := range messages {
		if hidden[i] {
			continue
		}
		n += msgChars(m)
	}
	return n
}

// EstimateTokens 粗略估算消息列表的 token 数（约 4 字符/token）。
func EstimateTokens(messages []llm.Message) int {
	return totalChars(messages) / 4
}

// Summary 返回压缩前后的简短描述（用于日志）。
func Summary(before, after []llm.Message) string {
	return fmt.Sprintf("消息 %d→%d 条, 约 %d→%d tokens",
		len(before), len(after), EstimateTokens(before), EstimateTokens(after))
}
