package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	"github.com/jakartasipirok/AfterThought/internal/model"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func envTest(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func TestIntegrationEmptyLibraryToolLoopAndJournal(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires local Postgres, Temporal and Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dsn := envTest("DATABASE_URL", "postgres://afterthought:afterthought@127.0.0.1:55432/afterthought?sslmode=disable")
	admin, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Close()
	schema := "test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &storage.DB{Pool: pool}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("../../.afterthought", "agent-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	files, err := storage.NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := filepath.Abs("../../harness/skills")
	if err != nil {
		t.Fatal(err)
	}
	image, err := harness.ResolveImage(ctx, envTest("AFTERTHOUGHT_SANDBOX_IMAGE", "python:3.12-bookworm"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Remember(ctx, "test-task", "human:reviewer", "A correction supplied as grounded human feedback.", "feedback"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
			Tools    []model.Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 4 {
			t.Error("expanded tool visibility")
		}
		if strings.Contains(request.Messages[0].Content, "Packaged nodes will implement") {
			t.Error("full skill body preloaded")
		}
		message := model.Message{Role: "assistant"}
		finish := "tool_calls"
		switch calls.Add(1) {
		case 1:
			message.ToolCalls = []model.Call{{ID: "write-1", Type: "function", Function: model.FunctionCall{Name: "write", Arguments: `{"path":"created.txt","content":"created-from-empty-library"}`}}}
		case 2:
			message.ToolCalls = []model.Call{{ID: "bash-1", Type: "function", Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"cat created.txt"}`}}}
		default:
			finish = "stop"
			message.Content = "Created a file and verified it using Bash."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
	}))
	defer server.Close()
	a := &Activities{DB: db, Files: files, Model: &model.Client{BaseURL: server.URL, Model: "mock"}, Skills: skills, Image: image}
	var retained atomic.Int32
	memoryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/memories/recall") {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "fact", "text": "Prior correction is attributed to its human reviewer.", "type": "experience", "metadata": map[string]string{"source": "human:reviewer"}}}})
			return
		}
		var request struct {
			Operation string `json:"operation_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		retained.Add(1)
		_ = json.NewEncoder(w).Encode(memory.Receipt{Success: true, Async: true, OperationID: request.Operation})
	}))
	defer memoryServer.Close()
	a.Hindsight = &memory.Hindsight{BaseURL: memoryServer.URL}
	c, err := client.Dial(client.Options{HostPort: envTest("TEMPORAL_ADDRESS", "127.0.0.1:7233")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := "afterthought-test-" + rand.Text()
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(AgentWorkflow)
	w.RegisterActivity(a)
	if err = w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	id := "agent-test-" + rand.Text()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: queue}, AgentWorkflow, Input{ID: id, Task: "test-task", Prompt: "Create a file from scratch and test it.", MaxTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	var out Outcome
	if err = run.Get(ctx, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" || out.Turns != 3 || calls.Load() != 3 {
		t.Fatalf("unexpected completion: %+v", out)
	}
	data, err := os.ReadFile(filepath.Join(a.workspace(id), "created.txt"))
	if err != nil || string(data) != "created-from-empty-library" {
		t.Fatalf("tool output missing: %v", err)
	}
	transcript, err := a.load(out.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(transcript)
	if !strings.Contains(string(serialized), "human:reviewer") || !strings.Contains(string(serialized), "created-from-empty-library") {
		t.Fatal("memory or tool evidence missing from transcript")
	}
	if !strings.Contains(string(serialized), "Hindsight recalled learning evidence") || retained.Load() != 2 {
		t.Fatalf("Hindsight recall/outcome flow failed: retained=%d", retained.Load())
	}
	state, cached, err := db.BeginTool(ctx, id, "write-1", graph.Digest(model.Call{ID: "write-1", Type: "function", Function: model.FunctionCall{Name: "write", Arguments: `{"path":"created.txt","content":"created-from-empty-library"}`}}))
	if err != nil || state != "completed" || cached == nil {
		t.Fatalf("committed result not reused: %s %v", state, err)
	}
	if _, _, err = db.BeginTool(ctx, id, "write-1", "other-arguments"); err == nil {
		t.Fatal("call ID collision ignored")
	}
	state, _, err = db.BeginTool(ctx, id, "uncertain-call", "request")
	if err != nil || state != "new" {
		t.Fatal(err)
	}
	state, _, err = db.BeginTool(ctx, id, "uncertain-call", "request")
	if err != nil || state != "uncertain" {
		t.Fatal("unknown side-effect outcome would rerun")
	}
	history := &historypb.History{}
	iterator := c.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, event)
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(AgentWorkflow)
	if err = replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("recorded session no longer replays: %v", err)
	}
}
