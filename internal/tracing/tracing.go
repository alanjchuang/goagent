// Package tracing 实现可选的 langfuse 追踪上报（纯 HTTP，无 SDK）。
//
// 对应 AgentLoom 的 langfuse 集成。通过 langfuse 的 /api/public/ingestion
// 批量接口，把 agent 执行事件以 trace + span/event 形式上报。
//
// 仅当环境变量 LANGFUSE_PUBLIC_KEY / LANGFUSE_SECRET_KEY 存在时启用。
// LANGFUSE_HOST 可覆盖默认 host(https://cloud.langfuse.com)。
package tracing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alanjchuang/goagent/internal/events"
	"github.com/alanjchuang/goagent/internal/logging"
)

// Tracer 订阅事件总线并把事件上报到 langfuse。
type Tracer struct {
	host      string
	publicKey string
	secretKey string
	traceID   string
	client    *http.Client

	mu      sync.Mutex
	batch   []map[string]any
	cancel  func()
	stopped chan struct{}
}

// EnabledFromEnv 返回是否配置了 langfuse 凭据。
func EnabledFromEnv() bool {
	return os.Getenv("LANGFUSE_PUBLIC_KEY") != "" && os.Getenv("LANGFUSE_SECRET_KEY") != ""
}

// Start 启动追踪器：创建一个 trace，订阅事件并周期性批量上报。
// 返回的 Tracer 需在结束时调用 Stop。若未配置凭据则返回 nil。
func Start(traceName string) *Tracer {
	if !EnabledFromEnv() {
		return nil
	}
	host := os.Getenv("LANGFUSE_HOST")
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	t := &Tracer{
		host:      host,
		publicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"),
		secretKey: os.Getenv("LANGFUSE_SECRET_KEY"),
		traceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		client:    &http.Client{Timeout: 10 * time.Second},
		stopped:   make(chan struct{}),
	}

	// 创建 trace 事件。
	t.enqueue(map[string]any{
		"id":        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
		"type":      "trace-create",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"body": map[string]any{
			"id":   t.traceID,
			"name": traceName,
		},
	})

	ch, cancel := events.Default().Subscribe()
	t.cancel = cancel
	go t.loop(ch)
	logging.Get().Info("langfuse 追踪已启用: trace=%s", t.traceID)
	return t
}

// loop 消费事件并定期 flush。
func (t *Tracer) loop(ch <-chan events.Event) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.flush()
				close(t.stopped)
				return
			}
			t.recordEvent(e)
		case <-ticker.C:
			t.flush()
		}
	}
}

// recordEvent 把一条事件转成 langfuse span/event。
func (t *Tracer) recordEvent(e events.Event) {
	t.enqueue(map[string]any{
		"id":        fmt.Sprintf("evt_%d_%s", time.Now().UnixNano(), e.Type),
		"type":      "event-create",
		"timestamp": e.Timestamp.UTC().Format(time.RFC3339Nano),
		"body": map[string]any{
			"id":      fmt.Sprintf("span_%d", time.Now().UnixNano()),
			"traceId": t.traceID,
			"name":    e.Type,
			"input":   map[string]any{"agent": e.AgentName, "step": e.Step},
			"output":  e.Detail,
		},
	})
}

func (t *Tracer) enqueue(item map[string]any) {
	t.mu.Lock()
	t.batch = append(t.batch, item)
	shouldFlush := len(t.batch) >= 20
	t.mu.Unlock()
	if shouldFlush {
		t.flush()
	}
}

// flush 把累积的事件批量 POST 到 langfuse ingestion 接口。
func (t *Tracer) flush() {
	t.mu.Lock()
	if len(t.batch) == 0 {
		t.mu.Unlock()
		return
	}
	batch := t.batch
	t.batch = nil
	t.mu.Unlock()

	payload, err := json.Marshal(map[string]any{"batch": batch})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, t.host+"/api/public/ingestion", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(t.publicKey, t.secretKey)
	resp, err := t.client.Do(req)
	if err != nil {
		logging.Get().Debug("langfuse 上报失败: %v", err)
		return
	}
	_ = resp.Body.Close()
}

// Stop 取消订阅、flush 剩余事件并等待退出。
func (t *Tracer) Stop() {
	if t == nil {
		return
	}
	if t.cancel != nil {
		t.cancel()
	}
	<-t.stopped
}
