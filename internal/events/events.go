// Package events 提供一个进程内事件总线，用于把 agent 执行过程中的关键事件
// 广播给订阅者（如 Web UI 的 SSE 推送）。
//
// 对应 AgentLoom src/ui 的事件采集层（简化版）。agent 在关键节点 Publish 事件，
// UI 服务器订阅后通过 SSE 实时推送到浏览器。
package events

import (
	"sync"
	"time"
)

// Event 是一条可视化事件。
type Event struct {
	Type      string    `json:"type"`       // step / tool_call / tool_result / final_answer / subagent_start / subagent_stop
	AgentName string    `json:"agent_name"` // 产生事件的 agent
	Detail    string    `json:"detail"`     // 事件详情（工具名、内容摘要等）
	Step      int       `json:"step"`       // 步数（如适用）
	Timestamp time.Time `json:"timestamp"`
}

// Bus 是一个简单的发布/订阅总线。
type Bus struct {
	mu          sync.RWMutex
	subscribers map[int]chan Event
	nextID      int
	history     []Event // 保留历史，供新订阅者回放
}

var global = &Bus{subscribers: make(map[int]chan Event)}

// Default 返回全局事件总线。
func Default() *Bus { return global }

// Publish 广播一条事件给所有订阅者，并记录到历史。
func (b *Bus) Publish(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	b.mu.Lock()
	b.history = append(b.history, e)
	// 限制历史上限，避免无限增长。
	if len(b.history) > 1000 {
		b.history = b.history[len(b.history)-1000:]
	}
	subs := make([]chan Event, 0, len(b.subscribers))
	for _, ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		// 非阻塞发送：订阅者慢时丢弃，避免拖垮 agent。
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe 注册一个订阅者，返回事件通道与取消函数。
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan Event, 64)
	b.subscribers[id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(c)
		}
	}
	return ch, cancel
}

// History 返回已记录的历史事件副本（供新订阅者回放）。
func (b *Bus) History() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, len(b.history))
	copy(out, b.history)
	return out
}

// Publish 是发布到全局总线的便捷函数。
func Publish(e Event) { global.Publish(e) }
