// Package hooks 实现生命周期 Hook。
//
// 对应 AgentLoom src/lib/smolagents/hooks。Hook 是在 agent 生命周期事件
// （工具调用前后、任务开始/结束、子任务开始/结束）触发的外部命令。
// Hook 可来自 SKILL.md frontmatter 或 agent 配置。
//
// 本实现支持 "command" 类型 hook：执行一条 shell 命令，并通过环境变量与
// stdin(JSON) 传入事件上下文。command 的非零退出码会被记录但不中断 agent。
package hooks

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"sync"
	"time"

	"github.com/alanjchuang/goagent/internal/logging"
)

// Event 是 hook 的生命周期事件类型。
type Event string

const (
	PreToolUse    Event = "PreToolUse"
	PostToolUse   Event = "PostToolUse"
	TaskStart     Event = "TaskStart"
	TaskComplete  Event = "TaskComplete"
	TaskFail      Event = "TaskFail"
	SubagentStart Event = "SubagentStart"
	SubagentStop  Event = "SubagentStop"
)

// Hook 是一条已注册的 hook。
type Hook struct {
	Event   Event
	Matcher string // 工具名匹配模式（"*" 或具体工具名），仅工具类事件用
	Type    string // 目前支持 "command"
	Command string // command 类型的执行命令
	WorkDir string // 命令执行目录（通常为 skill 目录）
	Source  string // 来源（如 skill 名），便于日志
}

// Context 是触发 hook 时传入的上下文，序列化为 JSON 经 stdin 传给命令。
type Context struct {
	Event     string `json:"event"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolArgs  string `json:"tool_args,omitempty"`
	ToolResult string `json:"tool_result,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Task      string `json:"task,omitempty"`
}

// Manager 持有并触发已注册的 hook。
type Manager struct {
	mu    sync.Mutex
	hooks []Hook
}

// NewManager 创建空 Manager。
func NewManager() *Manager { return &Manager{} }

// Register 注册一条 hook。
func (m *Manager) Register(h Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = append(m.hooks, h)
}

// HasHooks 返回是否注册了任何 hook。
func (m *Manager) HasHooks() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hooks) > 0
}

// matches 判断 hook 是否应在该事件+工具名下触发。
func (h Hook) matches(event Event, toolName string) bool {
	if h.Event != event {
		return false
	}
	// 非工具事件无需匹配 matcher。
	if event != PreToolUse && event != PostToolUse {
		return true
	}
	if h.Matcher == "" || h.Matcher == "*" {
		return true
	}
	return h.Matcher == toolName
}

// Fire 触发某事件下匹配的所有 hook（同步执行，失败不中断）。
func (m *Manager) Fire(event Event, ctx Context) {
	m.mu.Lock()
	matched := make([]Hook, 0)
	for _, h := range m.hooks {
		if h.matches(event, ctx.ToolName) {
			matched = append(matched, h)
		}
	}
	m.mu.Unlock()

	for _, h := range matched {
		m.run(h, ctx)
	}
}

// run 执行单条 command hook。
func (m *Manager) run(h Hook, ctx Context) {
	if h.Type != "command" || h.Command == "" {
		return
	}
	payload, _ := json.Marshal(ctx)

	cmd := exec.Command("sh", "-c", h.Command)
	if h.WorkDir != "" {
		cmd.Dir = h.WorkDir
	}
	cmd.Stdin = bytes.NewReader(payload)
	// 通过环境变量也传一份关键字段，方便脚本读取。
	cmd.Env = append(cmd.Environ(),
		"HOOK_EVENT="+ctx.Event,
		"HOOK_TOOL_NAME="+ctx.ToolName,
		"HOOK_AGENT_NAME="+ctx.AgentName,
	)

	done := make(chan error, 1)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		logging.Get().Warn("hook[%s] 启动失败: %v", h.Source, err)
		return
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			logging.Get().Warn("hook[%s/%s] 执行失败: %v; 输出: %s", h.Source, h.Event, err, out.String())
		} else {
			logging.Get().Debug("hook[%s/%s] 执行完成", h.Source, h.Event)
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		logging.Get().Warn("hook[%s/%s] 超时被终止", h.Source, h.Event)
	}
}
