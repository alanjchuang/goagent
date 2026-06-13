// agent_config.go 解析 workflow YAML（即 applications/<app>/workflows/<agent>.yaml）。
// 对应 Python 版 YamlAgentFactory 加载的 agent 配置。
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ToolRef 是 agent YAML 中 tools 列表的一项。
type ToolRef struct {
	Name string `yaml:"name"`
}

// WorkerRef 是 worker_agents 列表的一项（指向另一个 yaml 文件）。
type WorkerRef struct {
	Path string `yaml:"path"`
}

// AgentConfig 描述一个 supervisor/worker agent 的 YAML 配置。
type AgentConfig struct {
	Name         string      `yaml:"name"`
	Description  string      `yaml:"description"`
	ModelType    string      `yaml:"model_type"`
	ToolCallType string      `yaml:"tool_call_type"` // "tool_call" | "code_act"
	Workflow     string      `yaml:"workflow"`
	Tools        []ToolRef   `yaml:"tools"`
	WorkerAgents []WorkerRef `yaml:"worker_agents"`

	// SourcePath 记录该配置来自哪个文件，便于解析相对路径。
	SourcePath string `yaml:"-"`
}

// LoadAgentConfig 从给定路径加载 agent 配置。相对路径相对 AgentRoot 解析。
func LoadAgentConfig(path string) (*AgentConfig, error) {
	resolved := path
	if !filepath.IsAbs(resolved) && C != nil {
		resolved = filepath.Join(C.AgentRoot, path)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("读取 agent 配置 %s 失败: %w", resolved, err)
	}
	var ac AgentConfig
	if err := yaml.Unmarshal(data, &ac); err != nil {
		return nil, fmt.Errorf("解析 agent 配置 %s 失败: %w", resolved, err)
	}
	ac.SourcePath = resolved
	if err := ac.validate(); err != nil {
		return nil, err
	}
	return &ac, nil
}

// validate 校验必填字段（对应 Python 版 validate_required_yaml_fields）。
func (a *AgentConfig) validate() error {
	var missing []string
	if a.Name == "" {
		missing = append(missing, "name")
	}
	if a.Description == "" {
		missing = append(missing, "description")
	}
	if a.Workflow == "" {
		missing = append(missing, "workflow")
	}
	if len(missing) > 0 {
		return fmt.Errorf("agent 配置 %s 缺少必填字段: %v", a.SourcePath, missing)
	}
	return nil
}
