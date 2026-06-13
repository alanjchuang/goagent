// Package ui 实现 loom ui 的 Web 可视化服务器。
//
// 对应 AgentLoom src/ui。启动一个 HTTP 服务器：
//   - GET /            返回内嵌的单页前端
//   - GET /events      SSE 端点，实时推送 agent 执行事件
//
// 事件来自 internal/events 全局总线。当用同一进程运行 agent 时，事件会实时
// 推送；本服务器也可独立启动用于观察（历史事件回放）。
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/alanjchuang/goagent/internal/events"
)

// indexHTML 是内嵌的可视化前端页面。
const indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>goagent 可视化</title>
<style>
  body { font-family: -apple-system, monospace; background:#1e1e2e; color:#cdd6f4; margin:0; padding:20px; }
  h1 { color:#89b4fa; font-size:18px; }
  #events { margin-top:16px; }
  .ev { padding:8px 12px; margin:4px 0; border-radius:6px; background:#313244; border-left:4px solid #89b4fa; }
  .ev .t { color:#a6e3a1; font-weight:bold; }
  .ev .a { color:#f9e2af; }
  .ev .ts { color:#6c7086; font-size:11px; float:right; }
  .ev.tool_call { border-left-color:#fab387; }
  .ev.tool_result { border-left-color:#a6e3a1; }
  .ev.final_answer { border-left-color:#f38ba8; }
  .ev.subagent_start, .ev.subagent_stop { border-left-color:#cba6f7; }
  #status { font-size:12px; color:#6c7086; }
</style>
</head>
<body>
  <h1>goagent 可视化面板 <span id="status">连接中...</span></h1>
  <div id="events"></div>
<script>
  const box = document.getElementById('events');
  const status = document.getElementById('status');
  const es = new EventSource('/events');
  es.onopen = () => { status.textContent = '● 已连接'; status.style.color = '#a6e3a1'; };
  es.onerror = () => { status.textContent = '● 断开'; status.style.color = '#f38ba8'; };
  es.onmessage = (m) => {
    const e = JSON.parse(m.data);
    const div = document.createElement('div');
    div.className = 'ev ' + e.type;
    const ts = new Date(e.timestamp).toLocaleTimeString();
    div.innerHTML = '<span class="ts">' + ts + '</span>' +
      '<span class="t">[' + e.type + ']</span> ' +
      '<span class="a">' + e.agent_name + '</span> ' +
      (e.step ? ('#' + e.step + ' ') : '') +
      escapeHtml(e.detail || '');
    box.appendChild(div);
    window.scrollTo(0, document.body.scrollHeight);
  };
  function escapeHtml(s){return s.replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));}
</script>
</body>
</html>`

// StartServer 在指定端口启动 Web UI 服务器（阻塞）。
func StartServer(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/events", handleSSE)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("goagent 可视化面板已启动: http://localhost:%d\n", port)
	return http.ListenAndServe(addr, mux)
}

// handleSSE 处理 SSE 订阅：先回放历史事件，再实时推送新事件。
func handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	bus := events.Default()

	// 回放历史。
	for _, e := range bus.History() {
		writeSSE(w, e)
	}
	flusher.Flush()

	ch, cancel := bus.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, e)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e events.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}
