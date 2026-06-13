// repo_map.go 实现 repo_map 工具：扫描仓库，提取各源码文件的顶层符号
// （函数、类型、类等），生成结构化的仓库地图。
//
// 对应 AgentLoom 的 repo_map 应用。原版用 tree-sitter 解析多语言；这里用
// 轻量的按语言正则提取，避免引入 cgo tree-sitter 依赖，覆盖常见语言。
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type RepoMap struct{}

func (RepoMap) Name() string { return "repo_map" }
func (RepoMap) Description() string {
	return "扫描指定目录，提取各源码文件的顶层符号(函数/类型/类)，生成仓库结构地图。" +
		"用于快速了解一个代码库的整体结构。"
}
func (RepoMap) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "要扫描的根目录，默认当前目录"},
			"max_files": map[string]any{"type": "integer", "description": "最多扫描的文件数，默认 200"},
		},
	}
}

// langRule 定义某类语言的文件后缀与符号提取正则。
type langRule struct {
	exts     []string
	patterns []*regexp.Regexp
}

var repoMapRules = []langRule{
	{
		exts: []string{".go"},
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s+)?([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)^type\s+([A-Za-z_]\w*)`),
		},
	},
	{
		exts: []string{".py"},
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^(?:async\s+)?def\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)^class\s+([A-Za-z_]\w*)`),
		},
	},
	{
		exts: []string{".js", ".ts", ".tsx", ".jsx"},
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)^(?:export\s+)?class\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)^(?:export\s+)?(?:const|let)\s+([A-Za-z_]\w*)\s*=\s*(?:async\s*)?\(`),
		},
	},
	{
		exts: []string{".java", ".kt"},
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)(?:public|private|protected)?\s*(?:static\s+)?class\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)(?:public|private|protected)\s+[\w<>\[\]]+\s+([A-Za-z_]\w*)\s*\(`),
		},
	},
	{
		exts: []string{".rs"},
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*(?:pub\s+)?fn\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`(?m)^\s*(?:pub\s+)?(?:struct|enum|trait)\s+([A-Za-z_]\w*)`),
		},
	},
}

func ruleForExt(ext string) (langRule, bool) {
	for _, r := range repoMapRules {
		for _, e := range r.exts {
			if e == ext {
				return r, true
			}
		}
	}
	return langRule{}, false
}

func (RepoMap) Execute(args map[string]any) (string, error) {
	root := strArg(args, "path")
	if root == "" {
		root = "."
	}
	if err := activePolicy.CheckPath(root); err != nil {
		return "", err
	}
	maxFiles := 200
	if v, ok := args["max_files"].(float64); ok && v > 0 {
		maxFiles = int(v)
	}

	type fileSyms struct {
		path string
		syms []string
	}
	var results []fileSyms
	count := 0

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" ||
				name == ".venv" || name == "__pycache__" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= maxFiles {
			return filepath.SkipAll
		}
		rule, ok := ruleForExt(filepath.Ext(p))
		if !ok {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		syms := extractSymbols(string(data), rule)
		if len(syms) > 0 {
			results = append(results, fileSyms{path: p, syms: syms})
			count++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "未在该目录下找到可解析的源码符号。", nil
	}

	sort.Slice(results, func(i, j int) bool { return results[i].path < results[j].path })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("仓库地图 (%s)，共 %d 个文件:\n\n", root, len(results)))
	for _, f := range results {
		sb.WriteString(f.path + "\n")
		for _, s := range f.syms {
			sb.WriteString("  - " + s + "\n")
		}
	}
	return sb.String(), nil
}

// extractSymbols 用规则的所有正则提取去重后的符号名。
func extractSymbols(content string, rule langRule) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range rule.patterns {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			name := m[1]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
			if len(out) >= 50 { // 单文件符号上限
				return out
			}
		}
	}
	return out
}
