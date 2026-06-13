// security.go 实现 shell 命令安全检查与路径权限校验。
//
// 对应 AgentLoom src/tools/shell/security.go 与 tool_access_control。
// 这里把安全策略集中在 tools 包内，供 shell_tool 及文件类工具复用。
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SecurityPolicy 描述 shell 与路径的安全策略。
type SecurityPolicy struct {
	// AllowedCommands 为允许的命令名白名单；含 "*" 或为空表示允许全部。
	AllowedCommands []string
	// BlockDestructive 为 true 时拦截危险破坏性命令。
	BlockDestructive bool
	// IncludePaths 允许访问的路径（支持 "*" 表示全部）。
	IncludePaths []string
	// ExcludePaths 禁止访问的路径（优先级高于 include）。
	ExcludePaths []string
}

// DefaultSecurityPolicy 返回较为宽松但拦截高危操作的默认策略。
func DefaultSecurityPolicy() SecurityPolicy {
	return SecurityPolicy{
		AllowedCommands:  []string{"*"},
		BlockDestructive: true,
		IncludePaths:     []string{"*"},
	}
}

// 危险命令模式（破坏性操作 / 提权 / 注入）。
var dangerousCmdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-rf\s+/`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\s+if=`),
	regexp.MustCompile(`>\s*/dev/sd[a-z]`),
	regexp.MustCompile(`:\(\)\s*\{.*\|.*&\s*\}`), // fork bomb
	regexp.MustCompile(`\bchmod\s+-R\s+777\s+/`),
	regexp.MustCompile(`\bsudo\b`),
}

// 命令替换 / 进程替换等注入风险。
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\$\(`),  // 命令替换 $()
	regexp.MustCompile("`"),     // 反引号命令替换
	regexp.MustCompile(`<\(`),   // 进程替换
	regexp.MustCompile(`\bLD_PRELOAD=`),
}

// CheckCommand 校验一条 shell 命令是否被策略允许。
func (p SecurityPolicy) CheckCommand(command string) error {
	if p.BlockDestructive {
		for _, re := range dangerousCmdPatterns {
			if re.MatchString(command) {
				return fmt.Errorf("命令被安全策略拦截: 匹配高危模式 %q", re.String())
			}
		}
		for _, re := range injectionPatterns {
			if re.MatchString(command) {
				return fmt.Errorf("命令被安全策略拦截: 含命令注入风险 %q", re.String())
			}
		}
	}
	if !containsWildcard(p.AllowedCommands) {
		name := firstCommandName(command)
		if !contains(p.AllowedCommands, name) {
			return fmt.Errorf("命令 %q 不在允许列表中", name)
		}
	}
	return nil
}

// CheckPath 校验一个文件路径是否被策略允许访问。
func (p SecurityPolicy) CheckPath(path string) error {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		abs = path
	}
	// exclude 优先。
	for _, ex := range p.ExcludePaths {
		if matchPath(ex, abs) {
			return fmt.Errorf("路径 %q 被排除规则禁止访问", path)
		}
	}
	// include 为空或含 "*" 则允许全部。
	if len(p.IncludePaths) == 0 || containsWildcard(p.IncludePaths) {
		return nil
	}
	for _, in := range p.IncludePaths {
		if matchPath(in, abs) {
			return nil
		}
	}
	return fmt.Errorf("路径 %q 不在允许访问范围内", path)
}

// ---- 辅助函数 ----

func firstCommandName(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsWildcard(list []string) bool {
	return contains(list, "*")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// matchPath 判断 abs 是否落在 pattern 指定的目录/glob 下。
func matchPath(pattern, abs string) bool {
	pattern = expandHome(pattern)
	if pattern == "*" {
		return true
	}
	pabs, err := filepath.Abs(pattern)
	if err != nil {
		pabs = pattern
	}
	// 目录前缀匹配。
	if abs == pabs || strings.HasPrefix(abs, pabs+string(os.PathSeparator)) {
		return true
	}
	// glob 匹配（对完整路径和 basename 都试一次）。
	if ok, _ := filepath.Match(pattern, abs); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(abs)); ok {
		return true
	}
	return false
}
