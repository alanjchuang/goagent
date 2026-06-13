// Package ui 实现 loom ui 的 Web 可视化服务器。
//
// 对应 AgentLoom src/ui。启动一个 HTTP 服务器：
//   - GET /                 返回内嵌的新手友好单页前端
//   - GET /events           SSE 端点，实时推送 agent 执行事件
//   - GET /api/workflows    返回 applications 下可执行的 workflow YAML 列表
//   - POST /api/run         创建一次异步 workflow/application 执行
//   - GET /api/runs         返回 Web UI 创建的执行历史
//   - GET /api/runs/{id}    返回某次执行的隔离结果
//   - POST /api/chat        直接和默认/指定模型对话
//   - GET /api/logs         返回本地 run.log 列表
//   - GET /api/logs/{agent}/{run} 返回某次运行的 run.log 文本
//
// 事件来自 internal/events 全局总线。当用同一进程运行 agent 时，事件会实时
// 推送；本服务器也可独立启动用于观察（历史事件回放）。
package ui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alanjchuang/goagent/internal/agent"
	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/events"
	"github.com/alanjchuang/goagent/internal/llm"
)

//go:embed index.html
var indexHTML string

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
	ID        string `json:"id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// RunRecord 是 Web UI 中一次 application 执行的隔离历史记录。
type RunRecord struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Task       string `json:"task"`
	Status     string `json:"status"` // running / succeeded / failed
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type runStore struct {
	mu    sync.RWMutex
	runs  map[string]*RunRecord
	order []string
}

var uiRuns = &runStore{runs: make(map[string]*RunRecord)}

func (s *runStore) add(r *RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[r.ID] = r
	s.order = append([]string{r.ID}, s.order...)
	if len(s.order) > 100 {
		old := s.order[100:]
		s.order = s.order[:100]
		for _, id := range old {
			delete(s.runs, id)
		}
	}
}

func (s *runStore) list() []RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RunRecord, 0, len(s.order))
	for _, id := range s.order {
		if r, ok := s.runs[id]; ok {
			out = append(out, *r)
		}
	}
	return out
}

func (s *runStore) get(id string) (*RunRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, false
	}
	copy := *r
	return &copy, true
}

func (s *runStore) finish(id, status, result, errMsg string, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		r.Status = status
		r.Result = result
		r.Error = errMsg
		r.FinishedAt = time.Now().Format(time.RFC3339Nano)
		r.DurationMS = time.Since(started).Milliseconds()
	}
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
	mux.HandleFunc("/api/runs", handleRunsList)
	mux.HandleFunc("/api/runs/", handleRunDetail)
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
	name := filepath.Base(req.Path)
	if ac, err := config.LoadAgentConfig(req.Path); err == nil && ac.Name != "" {
		name = ac.Name
	}
	createdAt := time.Now()
	run := &RunRecord{
		ID:        fmt.Sprintf("run_%d", createdAt.UnixNano()),
		Path:      req.Path,
		Name:      name,
		Task:      strings.TrimSpace(req.Task),
		Status:    "running",
		CreatedAt: createdAt.Format(time.RFC3339Nano),
	}
	uiRuns.add(run)
	go executeRun(run.ID, req.Path, run.Task, createdAt)
	writeJSON(w, runResponse{ID: run.ID, Status: run.Status, CreatedAt: run.CreatedAt})
}

func executeRun(id, path, task string, started time.Time) {
	result, err := agent.RunApp(path, task)
	if err != nil {
		uiRuns.finish(id, "failed", "", err.Error(), started)
		return
	}
	uiRuns.finish(id, "succeeded", result, "", started)
}

func handleRunsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, uiRuns.list())
}

func handleRunDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if !safePathSegment(id) {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	run, ok := uiRuns.get(id)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, run)
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
