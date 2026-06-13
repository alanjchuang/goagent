// tools.go 提供 load_skill / list_skills 两个工具，桥接 skills.Registry
// 与 agent 的工具系统。它们实现 tools.Tool 接口（方法集匹配，无需导入 tools 包）。
//
// 对应 AgentLoom src/tools/skills/skill_tool.py。
package skills

import (
	"fmt"
	"strings"
)

// ListSkillsTool 列出当前可用的（非 hidden）skill。
type ListSkillsTool struct {
	Reg *Registry
}

func (*ListSkillsTool) Name() string        { return "list_skills" }
func (*ListSkillsTool) Description() string { return "列出当前可用的 skill 及其简介。" }
func (*ListSkillsTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *ListSkillsTool) Execute(args map[string]any) (string, error) {
	list := t.Reg.Listable()
	if len(list) == 0 {
		return "当前没有可用的 skill。", nil
	}
	var sb strings.Builder
	sb.WriteString("可用 skill 列表（用 load_skill 加载正文）:\n")
	for _, s := range list {
		sb.WriteString(fmt.Sprintf("- %s (v%s): %s\n", s.Name, s.Version, s.Description))
	}
	return sb.String(), nil
}

// LoadSkillTool 加载并返回指定 skill 的正文内容。
type LoadSkillTool struct {
	Reg *Registry
}

func (*LoadSkillTool) Name() string { return "load_skill" }
func (*LoadSkillTool) Description() string {
	return "加载指定 skill 的完整说明正文，获取其操作指南后再据此执行任务。"
}
func (*LoadSkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "要加载的 skill 名称"},
		},
		"required": []string{"name"},
	}
}
func (t *LoadSkillTool) Execute(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("缺少参数 name")
	}
	s, ok := t.Reg.Get(name)
	if !ok || s.Mode == ModeHidden {
		return "", fmt.Errorf("未找到名为 %q 的可用 skill", name)
	}
	return fmt.Sprintf("# Skill: %s (v%s)\n%s\n\n%s", s.Name, s.Version, s.Description, s.Body), nil
}
