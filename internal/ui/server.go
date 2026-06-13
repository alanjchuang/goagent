// Package ui 实现 loom ui 的 Web 可视化服务器。
//
// 对应 AgentLoom src/ui。启动一个 HTTP 服务器：
//   - GET /            返回内嵌的单页前端
//   - GET /events      SSE 端点，实时推送 agent 执行事件
//   - GET /api/logs    返回本地 run.log 列表
//   - GET /api/logs/{agent}/{run} 返回某次运行的 run.log 文本
//
// 事件来自 internal/events 全局总线。当用同一进程运行 agent 时，事件会实时
// 推送；本服务器也可独立启动用于观察（历史事件回放）。
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alanjchuang/goagent/internal/config"
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
  .tabs { margin-top:12px; }
  button { background:#313244; color:#cdd6f4; border:1px solid #45475a; border-radius:6px; padding:6px 10px; cursor:pointer; }
  button.active { background:#89b4fa; color:#11111b; }
  #events, #logs { margin-top:16px; }
  #logs { display:none; }
  .ev { padding:8px 12px; margin:4px 0; border-radius:6px; background:#313244; border-left:4px solid #89b4fa; }
  .ev .t { color:#a6e3a1; font-weight:bold; }
  .ev .a { color:#f9e2af; }
  .ev .ts { color:#6c7086; font-size:11px; float:right; }
  .ev.tool_call { border-left-color:#fab387; }
  .ev.tool_result { border-left-color:#a6e3a1; }
  .ev.final_answer { border-left-color:#f38ba8; }
  .ev.subagent_start, .ev.subagent_stop { border-left-color:#cba6f7; }
  #status { font-size:12px; color:#6c7086; }
  .log-item { padding:8px 12px; margin:4px 0; border-radius:6px; background:#313244; cursor:pointer; }
  .log-item:hover { background:#45475a; }
  .muted { color:#6c7086; font-size:12px; }
  pre { white-space:pre-wrap; word-break:break-word; background:#11111b; border:1px solid #313244; border-radius:6px; padding:12px; max-height:70vh; overflow:auto; }
</style>
</head>
<body>
  <h1>goagent 可视化面板 <span id="status">连接中...</span></h1>
  <div class="tabs">
    <button id="tabEvents" class="active" onclick="showTab('events')">实时事件</button>
    <button id="tabLogs" onclick="showTab('logs'); loadLogs();">本地日志</button>
  </div>
  <div id="events"></div>
  <div id="logs">
    <div class="muted">日志来源：.logs/&lt;agent&gt;/&lt;timestamp&gt;/run.log（运行时需使用 --log-to-file）</div>
    <div id="logList"></div>
    <pre id="logContent">选择一条日志查看内容。</pre>
  </div>
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
  function showTab(name){
    document.getElementById('events').style.display = name === 'events' ? 'block' : 'none';
    document.getElementById('logs').style.display = name === 'logs' ? 'block' : 'none';
    document.getElementById('tabEvents').className = name === 'events' ? 'active' : '';
    document.getElementById('tabLogs').className = name === 'logs' ? 'active' : '';
  }
  async function loadLogs(){
    const list = document.getElementById('logList');
    list.innerHTML = '<div class="muted">加载中...</div>';
    const resp = await fetch('/api/logs');
    const logs = await resp.json();
    if (!logs.length) { list.innerHTML = '<div class="muted">暂无本地 run.log。请用 --log-to-file 运行一次 agent。</div>'; return; }
    list.innerHTML = '';
    logs.forEach(l => {
      const div = document.createElement('div');
      div.className = 'log-item';
      div.textContent = l.agent + ' / ' + l.run + ' / ' + l.size + ' bytes';
      div.onclick = () => loadLog(l.agent, l.run);
      list.appendChild(div);
    });
  }
  async function loadLog(agent, run){
    const pre = document.getElementById('logContent');
    pre.textContent = '加载中...';
    const resp = await fetch('/api/logs/' + encodeURIComponent(agent) + '/' + encodeURIComponent(run));
    pre.textContent = await resp.text();
  }
</script>
</body>
</html>`

// LogSummary 是本地 run.log 的前端列表摘要。
type LogSummary struct {
	Agent string `json:"agent"`
	Run   string `json:"run"`
	Size  int64  `json:"size"`
}

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
	mux.HandleFunc("/api/logs", handleLogsList)
	mux.HandleFunc("/api/logs/", handleLogContent)

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

// handleLogsList 返回本地归档日志列表。
func handleLogsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	logs, err := listRunLogs(logRootDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(logs)
}

// handleLogContent 返回某条本地 run.log 内容。
func handleLogContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/logs/"), "/")
	if len(parts) != 2 || !safePathSegment(parts[0]) || !safePathSegment(parts[1]) {
		http.Error(w, "bad log path", http.StatusBadRequest)
		return
	}
	path := filepath.Join(logRootDir(), parts[0], parts[1], "run.log")
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func listRunLogs(root string) ([]LogSummary, error) {
	agents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var logs []LogSummary
	for _, agent := range agents {
		if !agent.IsDir() || !safePathSegment(agent.Name()) {
			continue
		}
		runs, err := os.ReadDir(filepath.Join(root, agent.Name()))
		if err != nil {
			continue
		}
		for _, run := range runs {
			if !run.IsDir() || run.Name() == "checkpoints" || !safePathSegment(run.Name()) {
				continue
			}
			info, err := os.Stat(filepath.Join(root, agent.Name(), run.Name(), "run.log"))
			if err != nil || info.IsDir() {
				continue
			}
			logs = append(logs, LogSummary{Agent: agent.Name(), Run: run.Name(), Size: info.Size()})
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].Agent == logs[j].Agent {
			return logs[i].Run > logs[j].Run
		}
		return logs[i].Agent < logs[j].Agent
	})
	return logs, nil
}

func logRootDir() string {
	if config.C != nil && config.C.System.Logging.Dir != "" {
		return config.C.System.Logging.Dir
	}
	return ".logs"
}

func safePathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\\`)
}
