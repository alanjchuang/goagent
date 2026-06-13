// register.go 把 SKILL.md frontmatter 中的 hooks 段解析并注册到 Manager。
//
// SKILL.md 的 hooks 结构（兼容 Claude Code 格式）：
//
//	hooks:
//	  PreToolUse:
//	    - matcher: "*"
//	      hooks:
//	        - type: command
//	          command: python ./scripts/x.py
//	  TaskCompleted:
//	    - hooks:
//	        - type: command
//	          command: python ./scripts/y.py
package hooks

// eventAlias 把 SKILL.md 中的事件名归一化到本包的 Event。
var eventAlias = map[string]Event{
	"PreToolUse":         PreToolUse,
	"PostToolUse":        PostToolUse,
	"PostToolUseFailure": PostToolUse,
	"TaskStart":          TaskStart,
	"TaskCreated":        TaskStart,
	"TaskComplete":       TaskComplete,
	"TaskCompleted":      TaskComplete,
	"TaskFail":           TaskFail,
	"StopFailure":        TaskFail,
	"SubagentStart":      SubagentStart,
	"SubagentStop":       SubagentStop,
}

// RegisterFromSkill 解析一个 skill 的 hooks 段并注册到 Manager。
// rawHooks 为 SKILL.md frontmatter 中 hooks 字段的原始值（map[event][]group）。
// workDir 为 skill 目录（command 的执行目录），source 为 skill 名（日志用）。
func (m *Manager) RegisterFromSkill(rawHooks map[string]any, workDir, source string) {
	for eventName, groupsRaw := range rawHooks {
		event, ok := eventAlias[eventName]
		if !ok {
			continue
		}
		groups := toSlice(groupsRaw)
		for _, g := range groups {
			gm := toMap(g)
			matcher := asString(gm["matcher"])
			for _, hRaw := range toSlice(gm["hooks"]) {
				hm := toMap(hRaw)
				typ := asString(hm["type"])
				command := asString(hm["command"])
				if typ == "" || command == "" {
					continue
				}
				m.Register(Hook{
					Event:   event,
					Matcher: matcher,
					Type:    typ,
					Command: command,
					WorkDir: workDir,
					Source:  source,
				})
			}
		}
	}
}

// ---- 以下为 yaml.v3 解出的 any 的安全转换辅助 ----

func toSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func toMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out
	}
	return map[string]any{}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
