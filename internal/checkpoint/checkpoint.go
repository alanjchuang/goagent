// Package checkpoint 实现任务状态与对话历史的持久化与恢复。
//
// 对应 AgentLoom src/lib/checkpoint。每个任务保存到
// .logs/<agent>/checkpoints/<task_id>/state.json，包含任务文本、状态、
// 时间戳与对话消息。支持 --resume 从断点恢复、list-tasks、clean-tasks。
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/alanjchuang/goagent/internal/llm"
)

// Status 是任务状态。
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// State 是一个任务的完整持久化状态。
type State struct {
	TaskID    string        `json:"task_id"`
	AgentName string        `json:"agent_name"`
	YAMLPath  string        `json:"yaml_path"`
	Task      string        `json:"task"`
	Status    Status        `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []llm.Message `json:"messages"`
	Result    string        `json:"result,omitempty"`
}

// Manager 管理某个 agent 的 checkpoint。
type Manager struct {
	agentName string
	baseDir   string // checkpoints 根目录
}

// NewManager 创建针对 agentName 的 checkpoint 管理器。
// logDir 为日志根目录（通常 ".logs"）。
func NewManager(agentName, logDir string) *Manager {
	return &Manager{
		agentName: agentName,
		baseDir:   filepath.Join(logDir, agentName, "checkpoints"),
	}
}

func (m *Manager) taskDir(taskID string) string {
	return filepath.Join(m.baseDir, taskID)
}

func (m *Manager) statePath(taskID string) string {
	return filepath.Join(m.taskDir(taskID), "state.json")
}

// NewTaskID 生成一个新的 task id。
func NewTaskID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}

// Save 持久化任务状态。
func (m *Manager) Save(s *State) error {
	s.UpdatedAt = time.Now()
	if err := os.MkdirAll(m.taskDir(s.TaskID), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：先写临时文件再 rename。
	tmp := m.statePath(s.TaskID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath(s.TaskID))
}

// Load 读取指定任务的状态。
func (m *Manager) Load(taskID string) (*State, error) {
	data, err := os.ReadFile(m.statePath(taskID))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("解析 checkpoint %s 失败: %w", taskID, err)
	}
	return &s, nil
}

// Delete 删除某任务的 checkpoint。
func (m *Manager) Delete(taskID string) error {
	return os.RemoveAll(m.taskDir(taskID))
}

// List 列出该 agent 下所有任务状态，按更新时间倒序。
func (m *Manager) List() ([]*State, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var states []*State
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if s, err := m.Load(e.Name()); err == nil {
			states = append(states, s)
		}
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].UpdatedAt.After(states[j].UpdatedAt)
	})
	return states, nil
}

// CleanOlderThan 删除早于 maxAge 的已完成/失败任务，返回删除数量。
// maxAge<=0 表示删除全部。
func (m *Manager) CleanOlderThan(maxAge time.Duration) (int, error) {
	states, err := m.List()
	if err != nil {
		return 0, err
	}
	now := time.Now()
	removed := 0
	for _, s := range states {
		if maxAge > 0 && now.Sub(s.UpdatedAt) < maxAge {
			continue
		}
		if err := m.Delete(s.TaskID); err == nil {
			removed++
		}
	}
	return removed, nil
}

// ListAll 扫描 logDir 下所有 agent 的所有任务（用于 loom list-tasks）。
func ListAll(logDir string) ([]*State, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var all []*State
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := NewManager(e.Name(), logDir)
		states, _ := m.List()
		all = append(all, states...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	return all, nil
}
