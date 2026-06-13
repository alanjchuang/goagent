// Package toolparse 实现多策略 tool_call 文本解析。
//
// 对应 Python 版 src/lib/smolagents/monkey_patch/tool_call_parsing_patch.py（精简版）。
// 当 LLM 不返回原生 tool_calls 字段、而是把工具调用写在文本内容里时，
// 用一组按优先级排列的策略尝试从文本中提取出 (工具名, 参数JSON)。
//
// 策略优先级（命中即返回）：
//  1. standardJSON   —— 文本本身或代码块内是标准 JSON: {"name":..,"arguments":..}
//  2. fixedJSON      —— 修复常见非法 JSON（单引号、尾逗号、Python True/False/None）后再解析
//  3. xmlTags        —— XML 风格: <tool_call>{...}</tool_call> 或 <invoke name="x"><parameter ...>
//  4. regexExtract   —— 自由文本: Action: x / Action Input: {...}，或 函数名({...})
package toolparse

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ParsedCall 是从文本中解析出的一次工具调用。
type ParsedCall struct {
	Name      string // 工具名
	Arguments string // 参数（JSON 字符串）
	Strategy  string // 命中的策略名（用于日志/调试）
}

// strategy 是单个解析策略。
type strategy struct {
	name string
	fn   func(text string, knownTools map[string]bool) (*ParsedCall, bool)
}

// Parse 依次尝试各策略，返回首个成功解析且工具名已知的结果。
// knownTools 为已注册的工具名集合，用于校验解析结果是否为真实工具。
func Parse(text string, knownTools map[string]bool) (*ParsedCall, bool) {
	strategies := []strategy{
		{"standard_json", parseStandardJSON},
		{"fixed_json", parseFixedJSON},
		{"xml_tags", parseXMLTags},
		{"regex_extraction", parseRegex},
	}
	for _, s := range strategies {
		if call, ok := s.fn(text, knownTools); ok {
			call.Strategy = s.name
			return call, true
		}
	}
	return nil, false
}

// codeBlockRe 匹配 ```...``` 或 ```json ... ``` 代码块。
var codeBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// candidateBlocks 返回待解析的候选文本块：原文 + 各代码块内容。
func candidateBlocks(text string) []string {
	blocks := []string{strings.TrimSpace(text)}
	for _, m := range codeBlockRe.FindAllStringSubmatch(text, -1) {
		blocks = append(blocks, strings.TrimSpace(m[1]))
	}
	return blocks
}

// extractJSONObject 从字符串中提取第一个平衡的 {...} 子串（处理字符串内的花括号）。
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// callFromJSONMap 把一个解析出的 map 转成 ParsedCall（识别 name/arguments 字段的多种写法）。
func callFromJSONMap(m map[string]any, knownTools map[string]bool) (*ParsedCall, bool) {
	// 识别工具名字段：name / tool / tool_name / function.name
	name := ""
	for _, k := range []string{"name", "tool", "tool_name", "action"} {
		if v, ok := m[k].(string); ok && v != "" {
			name = v
			break
		}
	}
	// OpenAI 嵌套结构 {"function": {"name":..,"arguments":..}}
	if name == "" {
		if fn, ok := m["function"].(map[string]any); ok {
			if v, ok := fn["name"].(string); ok {
				name = v
			}
			if args, ok := fn["arguments"]; ok {
				return buildCall(name, args, knownTools)
			}
		}
	}
	if name == "" || !knownTools[name] {
		return nil, false
	}
	// 识别参数字段：arguments / args / parameters / input / tool_input / action_input
	for _, k := range []string{"arguments", "args", "parameters", "input", "tool_input", "action_input"} {
		if args, ok := m[k]; ok {
			return buildCall(name, args, knownTools)
		}
	}
	// 没有独立参数字段：把除 name 外的其余键当作参数。
	rest := map[string]any{}
	for k, v := range m {
		if k == "name" || k == "tool" || k == "tool_name" || k == "action" {
			continue
		}
		rest[k] = v
	}
	b, _ := json.Marshal(rest)
	return &ParsedCall{Name: name, Arguments: string(b)}, true
}

// buildCall 把参数（可能是 string 或 object）规整成 JSON 字符串。
func buildCall(name string, args any, knownTools map[string]bool) (*ParsedCall, bool) {
	if name == "" || !knownTools[name] {
		return nil, false
	}
	switch v := args.(type) {
	case string:
		// 参数本身是 JSON 字符串，原样使用（容错：非法则包成空对象）。
		if json.Valid([]byte(v)) {
			return &ParsedCall{Name: name, Arguments: v}, true
		}
		return &ParsedCall{Name: name, Arguments: "{}"}, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return &ParsedCall{Name: name, Arguments: string(b)}, true
	}
}

// ---- 策略 1: 标准 JSON ----

func parseStandardJSON(text string, knownTools map[string]bool) (*ParsedCall, bool) {
	for _, block := range candidateBlocks(text) {
		jsonStr, ok := extractJSONObject(block)
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			continue
		}
		if call, ok := callFromJSONMap(m, knownTools); ok {
			return call, true
		}
	}
	return nil, false
}

// ---- 策略 2: 修复后 JSON ----

func parseFixedJSON(text string, knownTools map[string]bool) (*ParsedCall, bool) {
	for _, block := range candidateBlocks(text) {
		jsonStr, ok := extractJSONObject(block)
		if !ok {
			continue
		}
		fixed := fixJSON(jsonStr)
		var m map[string]any
		if err := json.Unmarshal([]byte(fixed), &m); err != nil {
			continue
		}
		if call, ok := callFromJSONMap(m, knownTools); ok {
			return call, true
		}
	}
	return nil, false
}

var (
	trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)
	pyTrueRe        = regexp.MustCompile(`\bTrue\b`)
	pyFalseRe       = regexp.MustCompile(`\bFalse\b`)
	pyNoneRe        = regexp.MustCompile(`\bNone\b`)
)

// fixJSON 修复常见的非法 JSON：单引号、尾逗号、Python 字面量。
func fixJSON(s string) string {
	// 单引号 → 双引号（简单替换，已在双引号字符串内的单引号场景较少见）。
	s = strings.ReplaceAll(s, "'", "\"")
	s = trailingCommaRe.ReplaceAllString(s, "$1")
	s = pyTrueRe.ReplaceAllString(s, "true")
	s = pyFalseRe.ReplaceAllString(s, "false")
	s = pyNoneRe.ReplaceAllString(s, "null")
	return s
}

// ---- 策略 3: XML 标签 ----

var (
	// <tool_call>...</tool_call>（可带任意命名空间前缀，如 <minimax:tool_call>）
	toolCallTagRe = regexp.MustCompile(`(?s)<(?:[\w]+:)?tool_call>(.*?)</(?:[\w]+:)?tool_call>`)
	// <invoke name="x"> ... </invoke>
	invokeRe = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"\s*>(.*?)</invoke>`)
	// <parameter name="k">v</parameter>
	paramRe = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"\s*>(.*?)</parameter>`)
)

func parseXMLTags(text string, knownTools map[string]bool) (*ParsedCall, bool) {
	// 形式一：<tool_call>{json}</tool_call>
	if m := toolCallTagRe.FindStringSubmatch(text); m != nil {
		inner := strings.TrimSpace(m[1])
		if jsonStr, ok := extractJSONObject(inner); ok {
			var mp map[string]any
			if err := json.Unmarshal([]byte(fixJSON(jsonStr)), &mp); err == nil {
				if call, ok := callFromJSONMap(mp, knownTools); ok {
					return call, true
				}
			}
		}
	}
	// 形式二：<invoke name="x"><parameter name="k">v</parameter>...</invoke>
	if m := invokeRe.FindStringSubmatch(text); m != nil {
		name := strings.TrimSpace(m[1])
		if knownTools[name] {
			params := map[string]any{}
			for _, p := range paramRe.FindAllStringSubmatch(m[2], -1) {
				params[strings.TrimSpace(p[1])] = strings.TrimSpace(p[2])
			}
			b, _ := json.Marshal(params)
			return &ParsedCall{Name: name, Arguments: string(b)}, true
		}
	}
	return nil, false
}

// ---- 策略 4: 正则自由文本 ----

var (
	// Action: tool_name\nAction Input: {...}
	actionRe = regexp.MustCompile(`(?s)Action\s*:\s*([\w\-]+).*?Action\s+Input\s*:\s*(\{.*\})`)
	// 函数调用风格: tool_name({...})
	funcCallRe = regexp.MustCompile(`(?s)([\w\-]+)\s*\(\s*(\{.*\})\s*\)`)
)

func parseRegex(text string, knownTools map[string]bool) (*ParsedCall, bool) {
	if m := actionRe.FindStringSubmatch(text); m != nil {
		name := strings.TrimSpace(m[1])
		if knownTools[name] && json.Valid([]byte(m[2])) {
			return &ParsedCall{Name: name, Arguments: m[2]}, true
		}
	}
	if m := funcCallRe.FindStringSubmatch(text); m != nil {
		name := strings.TrimSpace(m[1])
		if knownTools[name] {
			args := m[2]
			if !json.Valid([]byte(args)) {
				args = fixJSON(args)
			}
			if json.Valid([]byte(args)) {
				return &ParsedCall{Name: name, Arguments: args}, true
			}
		}
	}
	return nil, false
}
