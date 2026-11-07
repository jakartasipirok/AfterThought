// Package webapi exposes the local operator UI without expanding the agent's
// four-tool surface. It serves DTOs, never provider credentials or reasoning items.
package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	"github.com/jakartasipirok/AfterThought/internal/model"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type Server struct {
	DB        *storage.DB
	Files     *storage.Files
	Temporal  client.Client
	Queue     string
	Hindsight *memory.Hindsight
	Model     string
}
type Message struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []model.Call `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type Detail struct {
	Session         storage.WebSession   `json:"session"`
	ExecutionStatus string               `json:"execution_status"`
	Progress        agentruntime.Outcome `json:"progress"`
	Transcript      []Message            `json:"transcript"`
	Tools           []storage.WebTool    `json:"tools"`
	Usage           []storage.WebUsage   `json:"usage"`
	Files           []File               `json:"files"`
	Warning         string               `json:"warning,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/sessions", s.list)
	mux.HandleFunc("POST /api/sessions", s.start)
	mux.HandleFunc("GET /api/sessions/{id}", s.detail)
	mux.HandleFunc("GET /api/sessions/{id}/files", s.file)
	mux.HandleFunc("GET /api/tasks/{task}/memory", s.taskMemory)
	mux.HandleFunc("POST /api/tasks/{task}/feedback", s.feedback)
	mux.HandleFunc("POST /api/tasks/{task}/recall", s.recall)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !loopbackHost(r.Host) {
			failure(w, http.StatusForbidden, "this API accepts only loopback hosts")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				failure(w, http.StatusForbidden, "cross-origin mutation rejected")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				if err != nil || u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
					failure(w, http.StatusForbidden, "cross-origin mutation rejected")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func loopbackHost(host string) bool {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.Trim(hostname, "[]")
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		failure(w, http.StatusUnsupportedMediaType, "use application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		failure(w, http.StatusBadRequest, "invalid or oversized JSON body")
		return false
	}
	if err = d.Decode(new(any)); err != io.EOF {
		failure(w, http.StatusBadRequest, "expected one JSON object")
		return false
	}
	return true
}
func (s *Server) queue() string {
	if s.Queue != "" {
		return s.Queue
	}
	return agentruntime.TaskQueue
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	database, temporal, worker := false, false, false
	pollers := 0
	if s.DB != nil && s.DB.Pool != nil {
		database = s.DB.Pool.Ping(ctx) == nil
	}
	if s.Temporal != nil {
		_, err := s.Temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
		temporal = err == nil
		if temporal {
			description, err := s.Temporal.DescribeTaskQueue(ctx, s.queue(), enums.TASK_QUEUE_TYPE_WORKFLOW)
			if err == nil {
				for _, poller := range description.GetPollers() {
					if stamp := poller.GetLastAccessTime(); stamp != nil && time.Since(stamp.AsTime()) < 90*time.Second {
						pollers++
						worker = true
					}
				}
			}
		}
	}
	ready := database && temporal && worker
	status, label := http.StatusOK, "ready"
	if !ready {
		status, label = http.StatusServiceUnavailable, "degraded"
	}
	respond(w, status, map[string]any{"status": label, "ready": ready, "checks": map[string]bool{"database": database, "temporal": temporal, "worker": worker}, "worker_pollers": pollers, "model": s.Model, "tools": []string{"read", "bash", "edit", "write"}, "memory_backend": map[bool]string{true: "hindsight", false: "local"}[s.Hindsight != nil]})
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			failure(w, 400, "limit must be 1..100")
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 10000 {
			failure(w, 400, "invalid cursor")
			return
		}
		offset = n
	}
	sessions, err := s.DB.WebSessions(r.Context(), limit, offset)
	if err != nil {
		failure(w, 503, "session journal unavailable")
		return
	}
	reply := map[string]any{"sessions": sessions}
	if len(sessions) == limit {
		reply["next_cursor"] = strconv.Itoa(offset + limit)
	}
	respond(w, 200, reply)
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID        string `json:"id"`
		Task      string `json:"task"`
		Prompt    string `json:"prompt"`
		Dataset   string `json:"dataset"`
		Objective string `json:"objective"`
		MaxTurns  int    `json:"max_turns"`
	}
	if !decode(w, r, &request) {
		return
	}
	if !identifier.MatchString(request.ID) {
		failure(w, 400, "supply a stable client-generated session ID (1..128 letters, numbers, dot, dash or underscore)")
		return
	}
	if request.Task == "" {
		request.Task = request.ID
	}
	if !identifier.MatchString(request.Task) {
		failure(w, 400, "invalid task ID")
		return
	}
	if request.Dataset != "" {
		problem, err := harness.NewProblem(request.Dataset, request.Objective)
		if err != nil {
			failure(w, 400, err.Error())
			return
		}
		data, _ := json.Marshal(problem)
		request.Prompt = "Discover the task and author a graph workflow for this dataset request. Surface missing evidence before claiming predictive accuracy. Request: " + string(data) + "\n" + request.Prompt
	}
	if strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 16<<10 {
		failure(w, 400, "prompt must contain 1..16384 bytes")
		return
	}
	if request.MaxTurns == 0 {
		request.MaxTurns = 12
	}
	if request.MaxTurns < 1 || request.MaxTurns > 32 {
		failure(w, 400, "max_turns must be 1..32")
		return
	}
	input := agentruntime.Input{ID: request.ID, Task: request.Task, Prompt: request.Prompt, MaxTurns: request.MaxTurns}
	digest := graph.Digest(input)
	created, err := s.DB.AdmitWebSession(r.Context(), input.ID, input.Task, digest, input)
	if errors.Is(err, storage.ErrAdmissionConflict) {
		failure(w, 409, err.Error())
		return
	}
	if err != nil {
		failure(w, 503, "could not journal session request")
		return
	}
	run, err := s.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{ID: input.ID, TaskQueue: s.queue(), WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionErrorWhenAlreadyStarted: true, Memo: map[string]interface{}{"afterthought_input_digest": digest}}, agentruntime.AgentWorkflow, input)
	runID := ""
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			description, describeErr := s.Temporal.DescribeWorkflowExecution(r.Context(), input.ID, already.RunId)
			if describeErr != nil {
				respond(w, 503, map[string]any{"error": "could not verify existing execution; retry the same ID", "id": input.ID, "saved": true})
				return
			}
			var existingDigest string
			payload := description.GetWorkflowExecutionInfo().GetMemo().GetFields()["afterthought_input_digest"]
			if payload == nil || converter.GetDefaultDataConverter().FromPayload(payload, &existingDigest) != nil || existingDigest != digest {
				if created {
					cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_ = s.DB.RejectWebAdmission(cleanup, input.ID, digest)
					cancel()
				}
				failure(w, 409, "session ID already belongs to an execution with an unverified or different request")
				return
			}
			runID = already.RunId
		} else {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.DB.WebLaunch(cleanup, input.ID, "", "Temporal start unavailable; retry with the same session ID")
			cancel()
			respond(w, 503, map[string]any{"error": "session saved but Temporal start is unavailable; retry the same ID", "id": input.ID, "saved": true})
			return
		}
	} else {
		runID = run.GetRunID()
	}
	if err = s.DB.WebLaunch(r.Context(), input.ID, runID, ""); err != nil {
		respond(w, 503, map[string]any{"error": "execution accepted but journal acknowledgement failed; retry the same ID", "id": input.ID, "saved": true})
		return
	}
	status := 202
	if !created {
		status = 200
	}
	respond(w, status, map[string]any{"id": input.ID, "task": input.Task, "status": "queued", "duplicate": !created})
}
func executionStatus(value enums.WorkflowExecutionStatus) string {
	switch value {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING:
		return "running"
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return "completed"
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED:
		return "failed"
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return "cancelled"
	case enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return "terminated"
	case enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "timed_out"
	default:
		return "unknown"
	}
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !identifier.MatchString(id) {
		failure(w, 400, "invalid session ID")
		return
	}
	session, err := s.DB.WebSession(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, 404, "session not found")
		return
	}
	if err != nil {
		failure(w, 503, "session journal unavailable")
		return
	}
	out := Detail{Session: session, ExecutionStatus: session.Status, Progress: agentruntime.Outcome{ID: id, Status: session.Status}, Transcript: []Message{}, Tools: []storage.WebTool{}, Usage: []storage.WebUsage{}, Files: []File{}}
	if s.Temporal != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		description, e := s.Temporal.DescribeWorkflowExecution(ctx, id, "")
		cancel()
		if e == nil {
			out.ExecutionStatus = executionStatus(description.GetWorkflowExecutionInfo().GetStatus())
			if out.ExecutionStatus == "running" {
				ctx, cancel = context.WithTimeout(r.Context(), time.Second)
				value, e := s.Temporal.QueryWorkflow(ctx, id, "", "progress")
				if e == nil {
					_ = value.Get(&out.Progress)
				}
				cancel()
			}
		} else {
			var missing *serviceerror.NotFound
			if !errors.As(e, &missing) || session.Status != "queued" {
				out.Warning = "Temporal state unavailable; showing the persisted journal"
			}
		}
	}
	names := map[string]string{}
	if session.Transcript.Digest != "" {
		data, e := s.Files.Get(session.Transcript)
		if e != nil {
			failure(w, 503, "transcript artifact unavailable")
			return
		}
		var messages []model.Message
		if e = graph.Decode(data, &messages); e != nil {
			failure(w, 503, "invalid transcript artifact")
			return
		}
		for _, message := range messages {
			for _, call := range message.ToolCalls {
				names[call.ID] = call.Function.Name
			}
			if message.Role == "system" || message.Role == "developer" {
				continue
			}
			out.Transcript = append(out.Transcript, Message{Role: message.Role, Content: message.Content, ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID})
		}
	}
	out.Tools, out.Usage, err = s.DB.WebJournal(r.Context(), id)
	if err != nil {
		failure(w, 503, "tool journal unavailable")
		return
	}
	for i := range out.Tools {
		out.Tools[i].Name = names[out.Tools[i].CallID]
	}
	out.Files, _ = s.files(id)
	respond(w, 200, out)
}
func (s *Server) workspace(id string) string {
	return filepath.Join(s.Files.Root, "workspaces", strings.TrimPrefix(graph.Hash([]byte(id)), "sha256:"))
}
func (s *Server) files(id string) ([]File, error) {
	out := []File{}
	root, err := os.OpenRoot(s.workspace(id))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if name == "skills" {
			return fs.SkipDir
		}
		if entry.IsDir() {
			if strings.Count(name, "/") >= 8 {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			return nil
		}
		if len(out) >= 200 {
			return fs.SkipAll
		}
		out = append(out, File{Path: name, Size: info.Size()})
		return nil
	})
	return out, err
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !identifier.MatchString(id) {
		failure(w, 400, "invalid session ID")
		return
	}
	if _, err := s.DB.WebSession(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		failure(w, 404, "session not found")
		return
	} else if err != nil {
		failure(w, 503, "session journal unavailable")
		return
	}
	name := r.URL.Query().Get("path")
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") {
		failure(w, 400, "use a relative file path")
		return
	}
	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") || name == "skills" || strings.HasPrefix(name, "skills/") {
		failure(w, 400, "file path is outside the session workspace")
		return
	}
	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			failure(w, 400, "invalid file offset")
			return
		}
		offset = value
	}
	root, err := os.OpenRoot(s.workspace(id))
	if err != nil {
		failure(w, 404, "file not found")
		return
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		failure(w, 404, "file not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		failure(w, 400, "file must be regular")
		return
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		failure(w, 400, "invalid file offset")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		failure(w, 503, "file read failed")
		return
	}
	truncated := len(data) > 64<<10
	if truncated {
		data = data[:64<<10]
	}
	respond(w, 200, map[string]any{"path": name, "content": strings.ToValidUTF8(string(data), "�"), "truncated": truncated, "next_offset": offset + int64(len(data))})
}
func taskID(w http.ResponseWriter, r *http.Request) (string, bool) {
	task := r.PathValue("task")
	if !identifier.MatchString(task) {
		failure(w, 400, "invalid task ID")
		return "", false
	}
	return task, true
}
func (s *Server) taskMemory(w http.ResponseWriter, r *http.Request) {
	task, ok := taskID(w, r)
	if !ok {
		return
	}
	records, err := s.DB.Memory(r.Context(), task)
	if err != nil {
		failure(w, 503, "memory journal unavailable")
		return
	}
	respond(w, 200, map[string]any{"records": records})
}
func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	task, ok := taskID(w, r)
	if !ok {
		return
	}
	var request struct {
		Actor   string `json:"actor"`
		Message string `json:"message"`
	}
	if !decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Actor) == "" || len(request.Actor) > 128 || strings.TrimSpace(request.Message) == "" || len(request.Message) > 4000 {
		failure(w, 400, "feedback requires actor and message (up to 4000 bytes)")
		return
	}
	key := graph.Hash([]byte(task + "/" + request.Actor + "/" + request.Message))
	if err := s.DB.Remember(r.Context(), task, "human:"+request.Actor, request.Message, key); err != nil {
		failure(w, 503, "could not save feedback")
		return
	}
	delivery := "local"
	warning := ""
	if s.Hindsight != nil {
		delivery = "accepted"
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		err := s.DB.SyncMemory(ctx, s.Hindsight, task)
		cancel()
		if err != nil {
			delivery = "pending"
			warning = "Feedback is saved in the journal; Hindsight delivery is pending."
		} else if accepted, lookupErr := s.DB.WebMemoryAccepted(r.Context(), key); lookupErr != nil || !accepted {
			delivery = "pending"
			warning = "Feedback is saved in the journal; Hindsight delivery is pending behind earlier outbox records."
		}
	}
	respond(w, 202, map[string]any{"saved": true, "memory_delivery": delivery, "warning": warning})
}
func (s *Server) recall(w http.ResponseWriter, r *http.Request) {
	task, ok := taskID(w, r)
	if !ok {
		return
	}
	var request struct {
		Query string `json:"query"`
	}
	if !decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Query) == "" || len(request.Query) > 4000 {
		failure(w, 400, "recall requires a query up to 4000 bytes")
		return
	}
	if s.Hindsight == nil {
		respond(w, 200, map[string]any{"facts": []memory.Fact{}, "backend": "local", "warning": "Hindsight is disabled in explicit local memory mode."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	facts, err := s.Hindsight.Recall(ctx, task, request.Query)
	if err != nil {
		failure(w, 503, "Hindsight recall unavailable")
		return
	}
	respond(w, 200, map[string]any{"facts": facts, "backend": "hindsight"})
}
