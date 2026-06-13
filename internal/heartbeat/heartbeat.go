// Package heartbeat 实现运行时心跳检测。
//
// 对应 AgentLoom src/lib/heartbeat。agent 运行时定期写心跳文件（含 pid 与
// 时间戳），供 list-tasks 判断一个 "running" 状态的任务是否其实已崩溃
// （进程已死或心跳过期）。
package heartbeat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Beat 是写入心跳文件的内容。
type Beat struct {
	PID       int       `json:"pid"`
	Timestamp time.Time `json:"timestamp"`
	AgentName string    `json:"agent_name"`
}

// Heartbeat 周期性写心跳文件。
type Heartbeat struct {
	path      string
	agentName string
	interval  time.Duration
	stop      chan struct{}
	wg        sync.WaitGroup
}

// New 创建一个写到 path 的心跳器。
func New(path, agentName string, interval time.Duration) *Heartbeat {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Heartbeat{
		path:      path,
		agentName: agentName,
		interval:  interval,
		stop:      make(chan struct{}),
	}
}

// Start 立即写一次并启动后台周期写入。
func (h *Heartbeat) Start() {
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	h.write()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
				h.write()
			}
		}
	}()
}

// Stop 停止心跳并等待后台 goroutine 退出。
func (h *Heartbeat) Stop() {
	select {
	case <-h.stop:
		// already closed
	default:
		close(h.stop)
	}
	h.wg.Wait()
}

func (h *Heartbeat) write() {
	b := Beat{PID: os.Getpid(), Timestamp: time.Now(), AgentName: h.agentName}
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, h.path)
	}
}

// DetectCrashed 读取心跳文件，判断任务是否已崩溃。
// 判定为崩溃的条件：心跳文件存在但进程已死，或心跳过期（超过 staleThreshold）。
// 返回 true 表示崩溃；文件不存在时返回 false（无法判断）。
func DetectCrashed(path string, staleThreshold time.Duration) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var b Beat
	if err := json.Unmarshal(data, &b); err != nil {
		return false
	}
	// 进程是否存活。
	if !processAlive(b.PID) {
		return true
	}
	// 心跳是否过期。
	if staleThreshold > 0 && time.Since(b.Timestamp) > staleThreshold {
		return true
	}
	return false
}

// processAlive 判断给定 pid 的进程是否存活（unix: 发送 signal 0）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// 在 unix 上 Signal(0) 可探测进程是否存在。
	return proc.Signal(syscall.Signal(0)) == nil
}
