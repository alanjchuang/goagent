// Package logging 提供带 agent 上下文的结构化日志。
//
// 对应 AgentLoom src/lib/logging。日志同时输出到终端和归档文件
// .logs/<agent>/<timestamp>/run.log。每条日志带 agent 名、级别、时间。
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level 是日志级别。
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "INFO"
	}
}

func parseLevel(s string) Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return DEBUG
	case "WARN", "WARNING":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return INFO
	}
}

// Logger 是带 agent 名的结构化日志器。
type Logger struct {
	agentName string
	minLevel  Level
	out       io.Writer // 终端
	file      *os.File  // 归档文件，可为 nil
	runDir    string    // 本次运行的日志目录
	mu        sync.Mutex
}

// 全局单例。
var (
	global     *Logger
	globalOnce sync.Once
)

// Options 控制日志器初始化。
type Options struct {
	AgentName string
	Level     string // "DEBUG"/"INFO"/"WARN"/"ERROR"
	Dir       string // 归档根目录，如 ".logs"；为空则不归档到文件
	ToFile    bool   // 是否写归档文件
}

// Init 初始化全局日志器（幂等：仅首次生效）。
func Init(opts Options) *Logger {
	globalOnce.Do(func() {
		l := &Logger{
			agentName: opts.AgentName,
			minLevel:  parseLevel(opts.Level),
			out:       os.Stdout,
		}
		if opts.ToFile && opts.Dir != "" {
			ts := time.Now().Format("20060102_150405")
			runDir := filepath.Join(opts.Dir, opts.AgentName, ts)
			if err := os.MkdirAll(runDir, 0o755); err == nil {
				if f, err := os.Create(filepath.Join(runDir, "run.log")); err == nil {
					l.file = f
					l.runDir = runDir
				}
			}
		}
		global = l
	})
	return global
}

// Get 返回全局日志器；若未初始化则用默认值初始化。
func Get() *Logger {
	if global == nil {
		return Init(Options{AgentName: "agent", Level: "INFO"})
	}
	return global
}

// RunDir 返回本次运行的归档目录（可能为空）。
func (l *Logger) RunDir() string { return l.runDir }

func (l *Logger) log(level Level, format string, args ...any) {
	if l == nil || level < l.minLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] [%s] %s",
		time.Now().Format("15:04:05.000"), level, l.agentName, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.out, line)
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
}

func (l *Logger) Debug(format string, args ...any) { l.log(DEBUG, format, args...) }
func (l *Logger) Info(format string, args ...any)  { l.log(INFO, format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.log(WARN, format, args...) }
func (l *Logger) Error(format string, args ...any) { l.log(ERROR, format, args...) }

// Close 关闭归档文件。
func (l *Logger) Close() {
	if l != nil && l.file != nil {
		_ = l.file.Close()
	}
}
