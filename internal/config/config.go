// Package config 负责加载和合并 goagent 的分层配置。
//
// 对应 Python 版的 src/lib/config/。提供一个全局对象 C，它持有：
//   - System: 来自 config/system.yaml 的运行时设置
//   - LLM:    来自 config/llm.yaml 的模型配置
//   - AgentRoot: 项目根目录（向上查找包含 config/ 的目录）
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ModelConfig 描述单个模型类型（powerful / fast / summary ...）的配置。
// 对应 Python 版 model_types.ModelConfig。
type ModelConfig struct {
	BaseURL                 string  `yaml:"base_url"`
	APIKey                  string  `yaml:"api_key"`
	Model                   string  `yaml:"model"` // litellm 风格的 model_id, 如 "openai/gpt-5"
	Temperature             float64 `yaml:"temperature"`
	MaxTokens               int     `yaml:"max_tokens"`
	Timeout                 int     `yaml:"timeout"`
	RequestsPerMinute       int     `yaml:"requests_per_minute"`
	ContextCache            bool    `yaml:"context_cache"`
	SupportsNativeToolCalls string  `yaml:"supports_native_tool_calls"` // "auto"|"true"|"false"
	NumRetries              int     `yaml:"num_retries"`
}

// LLMConfig 对应 config/llm.yaml 的 model 段。
type LLMConfig struct {
	DefaultModelType string                 `yaml:"default_model_type"`
	Models           map[string]ModelConfig `yaml:"-"` // 除 default_model_type 外的命名模型
}

// ForType 根据 model_type 返回对应的模型配置。
// 空字符串时回退到 default_model_type。
func (l *LLMConfig) ForType(modelType string) (ModelConfig, error) {
	if modelType == "" {
		modelType = l.DefaultModelType
	}
	mc, ok := l.Models[modelType]
	if !ok {
		return ModelConfig{}, fmt.Errorf("未知的 model_type %q，请检查 config/llm.yaml 中是否定义了该类型；可用类型: %v", modelType, l.modelNames())
	}
	return mc, nil
}

func (l *LLMConfig) modelNames() []string {
	names := make([]string, 0, len(l.Models))
	for k := range l.Models {
		names = append(names, k)
	}
	return names
}

// SystemConfig 对应 config/system.yaml 中我们当前需要的字段。
type SystemConfig struct {
	System struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"system"`
	Logging struct {
		Enabled bool   `yaml:"enabled"`
		Level   string `yaml:"level"`
		Dir     string `yaml:"dir"`
	} `yaml:"logging"`
	DefaultLoadedTools []string `yaml:"default_loaded_tools"`
}

// Config 是全局配置容器。
type Config struct {
	AgentRoot string
	System    SystemConfig
	LLM       LLMConfig
}

// C 是全局配置单例，调用 Load 后填充。
var C *Config

// Load 发现 agent_root 并加载 system.yaml 与 llm.yaml。
func Load() (*Config, error) {
	root, err := discoverAgentRoot()
	if err != nil {
		return nil, err
	}
	c := &Config{AgentRoot: root}

	// 加载 system.yaml
	sysPath := filepath.Join(root, "config", "system.yaml")
	if data, err := os.ReadFile(sysPath); err == nil {
		if err := yaml.Unmarshal(data, &c.System); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", sysPath, err)
		}
	}

	// 加载 llm.yaml（优先），回退到 llm.example.yaml
	llmPath := filepath.Join(root, "config", "llm.yaml")
	if _, err := os.Stat(llmPath); os.IsNotExist(err) {
		llmPath = filepath.Join(root, "config", "llm.example.yaml")
	}
	if err := loadLLM(llmPath, &c.LLM); err != nil {
		return nil, err
	}

	C = c
	return c, nil
}

// loadLLM 解析 llm.yaml 的 model 段。由于命名模型是动态键，需要两步解析。
func loadLLM(path string, out *LLMConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 LLM 配置 %s 失败: %w", path, err)
	}
	var raw struct {
		Model map[string]yaml.Node `yaml:"model"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	out.Models = make(map[string]ModelConfig)
	for key, node := range raw.Model {
		if key == "default_model_type" {
			_ = node.Decode(&out.DefaultModelType)
			continue
		}
		var mc ModelConfig
		if err := node.Decode(&mc); err != nil {
			return fmt.Errorf("解析模型 %q 失败: %w", key, err)
		}
		out.Models[key] = mc
	}
	return nil
}

// discoverAgentRoot 从当前工作目录向上查找包含 config/ 子目录的目录。
func discoverAgentRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "config")); err == nil && fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// 找不到则退回当前目录
	return os.Getwd()
}
