// Package ui 实现 loom ui 的 Web 可视化服务器。
//
// 对应 AgentLoom src/ui。启动一个 HTTP 服务器：
//   - GET /                 返回内嵌的新手友好单页前端
//   - GET /events           SSE 端点，实时推送 agent 执行事件
//   - GET /api/workflows    返回 applications 下可执行的 workflow YAML 列表
//   - POST /api/run         执行选中的 workflow/application
//   - POST /api/chat        直接和默认/指定模型对话
//   - GET /api/logs         返回本地 run.log 列表
//   - GET /api/logs/{agent}/{run} 返回某次运行的 run.log 文本
//
// 事件来自 internal/events 全局总线。当用同一进程运行 agent 时，事件会实时
// 推送；本服务器也可独立启动用于观察（历史事件回放）。
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/agent"
	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/events"
	"github.com/alanjchuang/goagent/internal/llm"
)

// indexHTML 是内嵌的新手友好可视化前端页面。
const indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>goagent Web UI</title>
<style>
  :root { color-scheme: dark; }
  body { font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; background:#11111b; color:#cdd6f4; margin:0; }
  header { padding:18px 24px; border-bottom:1px solid #313244; background:#181825; position:sticky; top:0; z-index:2; }
  h1 { margin:0; color:#89b4fa; font-size:22px; }
  .sub { color:#a6adc8; font-size:13px; margin-top:6px; }
  main { max-width:1100px; margin:0 auto; padding:20px; }
  .tabs { display:flex; flex-wrap:wrap; gap:8px; margin-bottom:16px; }
  button { background:#313244; color:#cdd6f4; border:1px solid #45475a; border-radius:8px; padding:9px 13px; cursor:pointer; font-size:14px; }
  button:hover { background:#45475a; }
  button.active, button.primary { background:#89b4fa; color:#11111b; border-color:#89b4fa; font-weight:600; }
  button:disabled { opacity:.55; cursor:not-allowed; }
  .panel { display:none; background:#181825; border:1px solid #313244; border-radius:12px; padding:16px; }
  .panel.active { display:block; }
  .grid { display:grid; grid-template-columns: 1fr 1fr; gap:16px; }
  @media (max-width: 760px) { .grid { grid-template-columns:1fr; } }
  label { display:block; color:#f9e2af; font-size:13px; margin:10px 0 6px; }
  input, select, textarea { width:100%; box-sizing:border-box; background:#11111b; color:#cdd6f4; border:1px solid #45475a; border-radius:8px; padding:10px; font-size:14px; }
  textarea { min-height:120px; resize:vertical; }
  .hint, .muted { color:#a6adc8; font-size:12px; line-height:1.5; }
  .status { color:#6c7086; font-size:12px; margin-left:8px; }
  .output, pre { white-space:pre-wrap; word-break:break-word; background:#11111b; border:1px solid #313244; border-radius:8px; padding:12px; max-height:70vh; overflow:auto; }
  .chat-box { display:flex; flex-direction:column; gap:10px; max-height:55vh; overflow:auto; padding:8px; background:#11111b; border:1px solid #313244; border-radius:8px; }
  .msg { padding:10px 12px; border-radius:10px; line-height:1.5; white-space:pre-wrap; }
  .msg.user { background:#1e3a5f; align-self:flex-end; max-width:82%; }
  .msg.assistant { background:#313244; align-self:flex-start; max-width:82%; }
  .msg .role { display:block; font-size:11px; color:#a6adc8; margin-bottom:4px; }
  .workflow-card, .log-item { padding:10px 12px; margin:6px 0; border-radius:8px; background:#313244; cursor:pointer; border:1px solid transparent; }
  .workflow-card:hover, .log-item:hover { background:#45475a; }
  .workflow-card.selected { border-color:#89b4fa; }
  .pill { display:inline-block; padding:2px 7px; border-radius:999px; background:#45475a; color:#cdd6f4; font-size:11px; margin-left:6px; }
  .ev { padding:8px 12px; margin:5px 0; border-radius:8px; background:#313244; border-left:4px solid #89b4fa; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:13px; }
  .ev .t { color:#a6e3a1; font-weight:bold; }
  .ev .a { color:#f9e2af; }
  .ev .ts { color:#6c7086; font-size:11px; float:right; }
  .ev.tool_call { border-left-color:#fab387; }
  .ev.tool_result { border-left-color:#a6e3a1; }
  .ev.final_answer { border-left-color:#f38ba8; }
  .danger { color:#f38ba8; }
</style>
</head>
<body>
<header>
  <h1>goagent Web UI <span id="conn" class="status">连接中...</span></h1>
  <div class="sub">给新手使用：可以直接和模型聊天，也可以选择 applications 里的 Agent 应用执行任务。</div>
</header>
<main>
  <div class="tabs">
    <button id="tabChat" class="active" onclick="showTab('chat')">💬 和模型对话</button>
    <button id="tabApps" onclick="showTab('apps'); loadWorkflows();">🚀 执行 Applications</button>
    <button id="tabEvents" onclick="showTab('events')">📡 实时事件</button>
    <button id="tabLogs" onclick="showTab('logs'); loadLogs();">📜 本地日志</button>
  </div>

  <section id="chat" class="panel active">
    <h2>和模型对话</h2>
    <div class="hint">适合纯聊天、问答、解释代码。这里不调用工具，只请求配置好的 LLM。</div>
    <label>模型类型</label>
    <select id="chatModel"><option value="">默认模型</option><option value="powerful">powerful</option><option value="fast">fast</option></select>
    <div id="chatBox" class="chat-box"><div class="muted">输入问题后点击发送。</div></div>
    <label>你的问题</label>
    <textarea id="chatInput" placeholder="例如：请用小白能懂的话解释一下这个项目是做什么的"></textarea>
    <button class="primary" id="chatSend" onclick="sendChat()">发送</button>
    <button onclick="clearChat()">清空对话</button>
    <span id="chatStatus" class="status"></span>
  </section>

  <section id="apps" class="panel">
    <h2>执行 Applications</h2>
    <div class="hint">选择一个 workflow YAML，填写任务，然后点击运行。运行过程可以切到“实时事件”查看工具调用。</div>
    <div class="grid">
      <div>
        <label>可执行应用</label>
        <div id="workflowList" class="output">加载中...</div>
      </div>
      <div>
        <label>已选应用路径</label>
        <input id="runPath" placeholder="applications/demo/workflows/demo_agent.yaml">
        <label>任务描述</label>
        <textarea id="runTask" placeholder="例如：分析当前项目，并用小白能懂的话总结"></textarea>
        <button class="primary" id="runBtn" onclick="runWorkflow()">运行应用</button>
        <span id="runStatus" class="status"></span>
        <label>执行结果</label>
        <div id="runOutput" class="output">尚未运行。</div>
      </div>
    </div>
  </section>

  <section id="events" class="panel">
    <h2>实时事件 <span id="eventHint" class="status">SSE 连接中...</span></h2>
    <div id="eventList"></div>
  </section>

  <section id="logs" class="panel">
    <h2>本地日志</h2>
    <div class="hint">日志来源：.logs/&lt;agent&gt;/&lt;timestamp&gt;/run.log。CLI 运行时需要加 --log-to-file 才会生成归档日志。</div>
    <div id="logList"></div>
    <pre id="logContent">选择一条日志查看内容。</pre>
  </section>
</main>
<script>
  let chatHistory = [];
  let selectedWorkflow = '';

  function esc(s){return String(s || '').replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));}
  function showTab(name){
    ['chat','apps','events','logs'].forEach(id => document.getElementById(id).classList.toggle('active', id === name));
    ['Chat','Apps','Events','Logs'].forEach(s => document.getElementById('tab'+s).classList.toggle('active', s.toLowerCase() === name));
  }
  async function postJSON(url, body){
    const resp = await fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
    const text = await resp.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch(e) { data = {error:text}; }
    if (!resp.ok) throw new Error(data.error || text || resp.statusText);
    return data;
  }

  function renderChat(){
    const box = document.getElementById('chatBox');
    if (!chatHistory.length) { box.innerHTML = '<div class="muted">输入问题后点击发送。</div>'; return; }
    box.innerHTML = chatHistory.map(m => '<div class="msg '+m.role+'"><span class="role">'+(m.role==='user'?'你':'模型')+'</span>'+esc(m.content)+'</div>').join('');
    box.scrollTop = box.scrollHeight;
  }
  async function sendChat(){
    const input = document.getElementById('chatInput');
    const msg = input.value.trim();
    if (!msg) return;
    chatHistory.push({role:'user', content:msg});
    input.value = '';
    renderChat();
    const btn = document.getElementById('chatSend');
    const st = document.getElementById('chatStatus');
    btn.disabled = true; st.textContent = '模型思考中...';
    try {
      const data = await postJSON('/api/chat', {model_type:document.getElementById('chatModel').value, messages:chatHistory});
      chatHistory.push({role:'assistant', content:data.answer || ''});
      renderChat();
      st.textContent = '完成，token 输入 '+(data.input_tokens||0)+' / 输出 '+(data.output_tokens||0);
    } catch(e) {
      st.innerHTML = '<span class="danger">失败：'+esc(e.message)+'</span>';
    } finally { btn.disabled = false; }
  }
  function clearChat(){ chatHistory = []; renderChat(); document.getElementById('chatStatus').textContent = ''; }

  async function loadWorkflows(){
    const list = document.getElementById('workflowList');
    list.textContent = '加载中...';
    try {
      const resp = await fetch('/api/workflows');
      const workflows = await resp.json();
      if (!workflows.length) { list.innerHTML = '<span class="muted">没有找到 applications 下的 workflow YAML。</span>'; return; }
      list.innerHTML = '';
      workflows.forEach(w => {
        const div = document.createElement('div');
        div.className = 'workflow-card' + (w.path === selectedWorkflow ? ' selected' : '');
        div.innerHTML = '<b>'+esc(w.name || w.path)+'</b><span class="pill">'+esc(w.tool_call_type||'tool_call')+'</span><span class="pill">'+esc(w.model_type||'default')+'</span><div class="muted">'+esc(w.path)+'</div><div>'+esc((w.description||'').slice(0,120))+'</div>';
        div.onclick = () => {
          selectedWorkflow = w.path;
          document.getElementById('runPath').value = w.path;
          document.getElementById('runTask').value = w.description || '';
          loadWorkflows();
        };
        list.appendChild(div);
      });
    } catch(e) { list.innerHTML = '<span class="danger">加载失败：'+esc(e.message)+'</span>'; }
  }
  async function runWorkflow(){
    const path = document.getElementById('runPath').value.trim();
    const task = document.getElementById('runTask').value.trim();
    if (!path) { alert('请选择或填写 workflow YAML 路径'); return; }
    const btn = document.getElementById('runBtn');
    const st = document.getElementById('runStatus');
    const out = document.getElementById('runOutput');
    btn.disabled = true; st.textContent = '运行中，可切到实时事件查看过程...'; out.textContent = '运行中...';
    try {
      const data = await postJSON('/api/run', {path:path, task:task});
      out.textContent = data.result || '';
      st.textContent = '完成，用时 '+(data.duration_ms||0)+' ms';
    } catch(e) {
      out.textContent = '执行失败：' + e.message;
      st.innerHTML = '<span class="danger">失败</span>';
    } finally { btn.disabled = false; }
  }

  const es = new EventSource('/events');
  es.onopen = () => { document.getElementById('conn').textContent = '● 已连接'; document.getElementById('eventHint').textContent = '已连接'; };
  es.onerror = () => { document.getElementById('conn').textContent = '● 断开'; document.getElementById('eventHint').textContent = '连接断开'; };
  es.onmessage = (m) => {
    const e = JSON.parse(m.data);
    const div = document.createElement('div');
    div.className = 'ev ' + e.type;
    const ts = new Date(e.timestamp).toLocaleTimeString();
    div.innerHTML = '<span class="ts">' + ts + '</span>' + '<span class="t">[' + esc(e.type) + ']</span> ' + '<span class="a">' + esc(e.agent_name) + '</span> ' + (e.step ? ('#' + e.step + ' ') : '') + esc(e.detail || '');
    document.getElementById('eventList').appendChild(div);
    div.scrollIntoView({block:'end'});
  };

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

// WorkflowSummary 是 applications 下可执行 workflow 的摘要。
type WorkflowSummary struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ModelType    string `json:"model_type"`
	ToolCallType string `json:"tool_call_type"`
}

type chatAPIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	ModelType string           `json:"model_type"`
	Messages  []chatAPIMessage `json:"messages"`
}

type chatResponse struct {
	Answer       string `json:"answer"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type runRequest struct {
	Path string `json:"path"`
	Task string `json:"task"`
}

type runResponse struct {
	Result     string `json:"result"`
	DurationMS int64  `json:"duration_ms"`
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
	mux.HandleFunc("/api/workflows", handleWorkflows)
	mux.HandleFunc("/api/run", handleRun)
	mux.HandleFunc("/api/chat", handleChat)
	mux.HandleFunc("/api/logs", handleLogsList)
	mux.HandleFunc("/api/logs/", handleLogContent)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("goagent Web UI 已启动: http://localhost:%d\n", port)
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

func handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := ensureConfig(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workflows, err := listWorkflows()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, workflows)
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := ensureConfig(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !isSafeWorkflowPath(req.Path) {
		http.Error(w, "workflow path must be under applications and end with .yaml/.yml", http.StatusBadRequest)
		return
	}
	start := time.Now()
	result, err := agent.RunApp(req.Path, strings.TrimSpace(req.Task))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, runResponse{Result: result, DurationMS: time.Since(start).Milliseconds()})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ensureConfig(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	client, err := llm.NewClient(strings.TrimSpace(req.ModelType))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "你是 goagent Web UI 中的友好助手，请用清晰、适合新手理解的方式回答。"}}
	for _, m := range req.Messages {
		role := llm.RoleUser
		if m.Role == "assistant" {
			role = llm.RoleAssistant
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		messages = append(messages, llm.Message{Role: role, Content: content})
	}
	if len(messages) == 1 {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	msg, err := client.Complete(ctx, messages, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, chatResponse{
		Answer:       msg.Content,
		InputTokens:  client.CumulativeUsage.InputTokens,
		OutputTokens: client.CumulativeUsage.OutputTokens,
	})
}

// handleLogsList 返回本地归档日志列表。
func handleLogsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := ensureConfig(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logs, err := listRunLogs(logRootDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, logs)
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

func listWorkflows() ([]WorkflowSummary, error) {
	root := filepath.Join(config.C.AgentRoot, "applications")
	var workflows []WorkflowSummary
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasSuffix(d.Name(), "_app") {
				return filepath.SkipDir
			}
			if d.Name() == "worker_agents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		rel, err := filepath.Rel(config.C.AgentRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ac, err := config.LoadAgentConfig(rel)
		if err != nil {
			return nil
		}
		workflows = append(workflows, WorkflowSummary{
			Path:         rel,
			Name:         ac.Name,
			Description:  strings.TrimSpace(ac.Description),
			ModelType:    ac.ModelType,
			ToolCallType: ac.ToolCallType,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(workflows, func(i, j int) bool { return workflows[i].Path < workflows[j].Path })
	return workflows, nil
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

func ensureConfig() error {
	if config.C != nil {
		return nil
	}
	_, err := config.Load()
	return err
}

func logRootDir() string {
	if config.C != nil && config.C.System.Logging.Dir != "" {
		return config.C.System.Logging.Dir
	}
	return ".logs"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func isSafeWorkflowPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.HasPrefix(path, "applications/") && isYAML(path) && !strings.Contains(path, "../")
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func safePathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\\`)
}
