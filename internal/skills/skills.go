// Package skills 实现 Skill 的加载与注入。
//
// 对应 AgentLoom src/lib/smolagents/skills 与 src/tools/skills。
// Skill 是一个目录，内含 SKILL.md（YAML frontmatter + markdown 正文）。
// frontmatter 字段：name / description / version / invocation-control。
//
// 三种调用模式（invocation-control.allow-model）：
//   - "force-inject": 启动时把 skill 正文强制注入系统提示词
//   - true:           按需加载；agent 可用 load_skill 工具读取正文
//   - false:          隐藏；对 model 不可见（通常只用于 hook 行为）
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode 表示 skill 对 model 的可见性。
type Mode string

const (
	ModeForceInject Mode = "force-inject"
	ModeOnDemand    Mode = "on-demand"
	ModeHidden      Mode = "hidden"
)

// Skill 是一个已加载的 skill。
type Skill struct {
	Name        string
	Description string
	Version     string
	Mode        Mode
	Body        string // SKILL.md 的正文（frontmatter 之后的部分）
	Dir         string // skill 目录绝对路径
}

// frontmatter 是 SKILL.md 头部的 YAML。
type frontmatter struct {
	Name             string `yaml:"name"`
	Description      string `yaml:"description"`
	Version          string `yaml:"version"`
	InvocationControl struct {
		AllowModel any `yaml:"allow-model"` // true / false / "force-inject"
	} `yaml:"invocation-control"`
}

// Registry 持有一组已加载的 skill。
type Registry struct {
	skills map[string]*Skill
}

// NewRegistry 创建空 registry。
func NewRegistry() *Registry {
	return &Registry{skills: make(map[string]*Skill)}
}

// LoadDir 扫描一个目录，把其下每个含 SKILL.md 的子目录加载为 skill。
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录不存在不算错误（skill 是可选的）。
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillMd := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skillMd); err != nil {
			continue
		}
		sk, err := loadSkillFile(skillMd)
		if err != nil {
			return fmt.Errorf("加载 skill %s 失败: %w", skillMd, err)
		}
		if _, exists := r.skills[sk.Name]; exists {
			return fmt.Errorf("skill 名称冲突: %q 已存在", sk.Name)
		}
		r.skills[sk.Name] = sk
	}
	return nil
}

// loadSkillFile 解析单个 SKILL.md。
func loadSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body := splitFrontmatter(string(data))

	var meta frontmatter
	if fm != "" {
		if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
			return nil, fmt.Errorf("解析 frontmatter 失败: %w", err)
		}
	}
	name := meta.Name
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	return &Skill{
		Name:        name,
		Description: meta.Description,
		Version:     meta.Version,
		Mode:        resolveMode(meta.InvocationControl.AllowModel),
		Body:        strings.TrimSpace(body),
		Dir:         filepath.Dir(path),
	}, nil
}

// resolveMode 把 allow-model 的值映射成 Mode。默认 on-demand。
func resolveMode(allowModel any) Mode {
	switch v := allowModel.(type) {
	case string:
		if v == "force-inject" {
			return ModeForceInject
		}
		if strings.EqualFold(v, "false") {
			return ModeHidden
		}
		return ModeOnDemand
	case bool:
		if v {
			return ModeOnDemand
		}
		return ModeHidden
	default:
		return ModeOnDemand
	}
}

// splitFrontmatter 分离 "---\n...\n---\n正文" 形式的 YAML frontmatter 与正文。
func splitFrontmatter(content string) (fm, body string) {
	content = strings.TrimLeft(content, "\ufeff \n\r\t")
	if !strings.HasPrefix(content, "---") {
		return "", content
	}
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", content
	}
	fm = rest[:idx]
	body = rest[idx+4:]
	// 去掉正文起始的换行
	body = strings.TrimPrefix(body, "\n")
	return strings.TrimSpace(fm), body
}

// Get 按名称取 skill。
func (r *Registry) Get(name string) (*Skill, bool) {
	s, ok := r.skills[name]
	return s, ok
}

// All 返回所有 skill（无序）。
func (r *Registry) All() []*Skill {
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}

// ForceInjected 返回所有 force-inject 模式的 skill 正文，用于拼进系统提示词。
func (r *Registry) ForceInjected() []*Skill {
	var out []*Skill
	for _, s := range r.skills {
		if s.Mode == ModeForceInject {
			out = append(out, s)
		}
	}
	return out
}

// Listable 返回对 model 可见（非 hidden）的 skill，用于 list_skills。
func (r *Registry) Listable() []*Skill {
	var out []*Skill
	for _, s := range r.skills {
		if s.Mode != ModeHidden {
			out = append(out, s)
		}
	}
	return out
}
