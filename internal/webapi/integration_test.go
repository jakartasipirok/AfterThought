package webapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
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
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	"github.com/jakartasipirok/AfterThought/internal/model"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func integrationEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// integrationServices isolates this API test from the demo journal and workers.
// Provider endpoints are httptest mocks; Postgres, Temporal, and Bash are real.
func integrationServices(t *testing.T, ctx context.Context) (*storage.DB, *storage.Files, client.Client, string, string) {
	t.Helper()
	dsn := integrationEnv("DATABASE_URL", "postgres://afterthought:afterthought@127.0.0.1:55432/afterthought?sslmode=disable")
	admin, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Pool.Close)
	schema := "web_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := &storage.DB{Pool: pool}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll("../../.afterthought", 0755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("../../.afterthought", "web-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	files, err := storage.NewFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := filepath.Abs("../../harness/skills")
	if err != nil {
		t.Fatal(err)
	}
	image, err := harness.ResolveImage(ctx, integrationEnv("AFTERTHOUGHT_SANDBOX_IMAGE", "python:3.12-bookworm"))
	if err != nil {
		t.Fatal(err)
	}
	temporal, err := client.Dial(client.Options{HostPort: integrationEnv("TEMPORAL_ADDRESS", "127.0.0.1:7233"), Namespace: integrationEnv("TEMPORAL_NAMESPACE", "default")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(temporal.Close)
	return db, files, temporal, skills, image
}

func integrationRequest(t *testing.T, ctx context.Context, httpClient *http.Client, method, endpoint string, body any) (int, []byte) {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

func integrationWorker(t *testing.T, temporal client.Client, queue string, activities *agentruntime.Activities) {
	t.Helper()
	w := worker.New(temporal, queue, worker.Options{})
	w.RegisterWorkflow(agentruntime.AgentWorkflow)
	w.RegisterActivity(activities)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
}

func TestIntegrationAPIAuthoringFeedbackAndRecall(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires local Postgres, Temporal, and Docker; providers are mocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, files, temporal, skills, image := integrationServices(t, ctx)
	const hiddenReasoning = "encrypted-reasoning-private-to-provider-replay"
	const evidence = "created-for-api-demo"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected provider route: %s", r.URL.Path)
			http.Error(w, "wrong provider route", http.StatusNotFound)
			return
		}
		var request struct {
			Store bool              `json:"store"`
			Tools []json.RawMessage `json:"tools"`
			Input json.RawMessage   `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 4 || request.Store {
			t.Error("API integration changed four-tool/stateless model contract")
		}
		output := []any{map[string]any{"id": "reasoning", "type": "reasoning", "encrypted_content": hiddenReasoning}}
		switch calls.Add(1) {
		case 1:
			output = append(output, map[string]any{"type": "function_call", "call_id": "api-write", "name": "write", "arguments": `{"path":"evidence.txt","content":"created-for-api-demo"}`})
		case 2:
			if !bytes.Contains(request.Input, []byte(hiddenReasoning)) {
				t.Error("opaque Responses state was not replayed")
			}
			output = append(output, map[string]any{"type": "function_call", "call_id": "api-bash", "name": "bash", "arguments": `{"command":"python -c 'from pathlib import Path; assert Path(\"evidence.txt\").read_text() == \"created-for-api-demo\"; print(\"verification passed\")'"}`})
		case 3:
			if !bytes.Contains(request.Input, []byte("verification passed")) {
				t.Error("real Bash evidence did not reach the final model turn")
			}
			output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Created evidence.txt and verified it using the sandbox."}}})
		default:
			t.Error("a retried session started duplicate model work")
			output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Unexpected duplicate work."}}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": output, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	}))
	t.Cleanup(provider.Close)
	var retained atomic.Int32
	var humanDelivered atomic.Bool
	memoryProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/memories/recall") {
			results := []any{}
			if humanDelivered.Load() {
				results = append(results,
					map[string]any{"id": "human-fact", "text": "The demo artifact passed its sandbox check.", "type": "experience", "metadata": map[string]string{"source": "human:demo-reviewer"}, "document_id": "feedback-document"},
					map[string]any{"id": "derived-pattern", "text": "Checking artifacts may catch authoring mistakes.", "type": "observation", "source_fact_ids": []string{"human-fact"}},
				)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "source_facts": map[string]any{"human-fact": map[string]any{"id": "human-fact", "text": "The demo artifact passed its sandbox check.", "metadata": map[string]string{"source": "human:demo-reviewer"}, "document_id": "feedback-document"}}})
			return
		}
		var request struct {
			Operation string `json:"operation_id"`
			Items     []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, item := range request.Items {
			if item.Metadata["source"] == "human:demo-reviewer" {
				humanDelivered.Store(true)
			}
		}
		retained.Add(1)
		_ = json.NewEncoder(w).Encode(memory.Receipt{Success: true, Async: true, OperationID: request.Operation})
	}))
	t.Cleanup(memoryProvider.Close)
	hindsight := &memory.Hindsight{BaseURL: memoryProvider.URL}
	queue := "afterthought-web-test-" + rand.Text()
	integrationWorker(t, temporal, queue, &agentruntime.Activities{DB: db, Files: files, Model: &model.Client{BaseURL: provider.URL, Model: "gpt-6.1-sol"}, Skills: skills, Image: image, Hindsight: hindsight})
	api := httptest.NewServer((&Server{DB: db, Files: files, Temporal: temporal, Queue: queue, Hindsight: hindsight, Model: "gpt-6.1-sol"}).Handler())
	t.Cleanup(api.Close)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	// A CLI or other caller may reserve the same Temporal ID before Setup has
	// written a session row. The API must not adopt that unrelated execution.
	collisionID := "api-collision-" + rand.Text()
	foreignRun, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: collisionID, TaskQueue: queue + "-unpolled"}, agentruntime.AgentWorkflow, agentruntime.Input{ID: collisionID, Task: "foreign-task", Prompt: "Queued by a different caller.", MaxTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := temporal.TerminateWorkflow(cleanup, foreignRun.GetID(), foreignRun.GetRunID(), "API collision test cleanup"); err != nil {
			t.Error(err)
		}
	})
	collisionStatus, collisionBody := integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/sessions", map[string]any{"id": collisionID, "task": "api-collision-task", "prompt": "A different API request must not adopt the queued workflow.", "max_turns": 4})
	if collisionStatus != http.StatusConflict || calls.Load() != 0 {
		t.Fatalf("queued non-API workflow adopted: status=%d calls=%d body=%s", collisionStatus, calls.Load(), collisionBody)
	}
	id := "api-test-" + rand.Text()
	task := "api-task-" + rand.Text()
	request := map[string]any{"id": id, "task": task, "prompt": "Create a small file from scratch and verify it using Bash.", "max_turns": 4}
	status, body := integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/sessions", request)
	if status != http.StatusAccepted || !bytes.Contains(body, []byte(id)) {
		t.Fatalf("start session: status=%d body=%s", status, body)
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/sessions", request)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"duplicate":true`)) {
		t.Fatalf("identical retry: status=%d body=%s", status, body)
	}
	changed := map[string]any{"id": id, "task": task, "prompt": "A different task must not reuse this paid session.", "max_turns": 4}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/sessions", changed)
	if status != http.StatusConflict {
		t.Fatalf("changed request reused session ID: status=%d body=%s", status, body)
	}
	var detail struct {
		Session struct {
			Status string `json:"status"`
			Result string `json:"result"`
		} `json:"session"`
		ExecutionStatus string            `json:"execution_status"`
		Transcript      []json.RawMessage `json:"transcript"`
		Tools           []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"tools"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	for {
		status, body = integrationRequest(t, ctx, httpClient, http.MethodGet, api.URL+"/api/sessions/"+id, nil)
		if status != http.StatusOK {
			t.Fatalf("poll accepted session: status=%d body=%s", status, body)
		}
		if err := json.Unmarshal(body, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(detail.ExecutionStatus, "completed") {
			break
		}
		if strings.EqualFold(detail.ExecutionStatus, "failed") || strings.EqualFold(detail.ExecutionStatus, "terminated") || strings.EqualFold(detail.ExecutionStatus, "canceled") {
			t.Fatalf("workflow failed through HTTP: %s", body)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out polling HTTP workflow: %s", body)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if detail.Session.Status != "completed" || !strings.Contains(detail.Session.Result, "Created evidence.txt") || calls.Load() != 3 {
		t.Fatalf("unexpected completed HTTP session: calls=%d body=%s", calls.Load(), body)
	}
	if bytes.Contains(body, []byte(hiddenReasoning)) || bytes.Contains(body, []byte("response_items")) || bytes.Contains(body, []byte("encrypted_content")) {
		t.Fatal("provider-only reasoning escaped into the browser DTO")
	}
	if len(detail.Transcript) == 0 || len(detail.Tools) != 2 || !bytes.Contains(body, []byte("verification passed")) {
		t.Fatalf("completed tool/transcript evidence missing: %s", body)
	}
	toolNames := map[string]bool{}
	for _, tool := range detail.Tools {
		if tool.Status != "completed" {
			t.Fatalf("unfinished journal tool in completed session: %+v", tool)
		}
		toolNames[tool.Name] = true
	}
	if !toolNames["write"] || !toolNames["bash"] {
		t.Fatalf("tool names missing from HTTP journal: %+v", toolNames)
	}
	foundFile := false
	for _, file := range detail.Files {
		foundFile = foundFile || file.Path == "evidence.txt"
	}
	if !foundFile {
		t.Fatal("generated file absent from HTTP workspace listing")
	}
	persisted, err := db.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := files.Get(persisted.Transcript)
	if err != nil || !bytes.Contains(transcript, []byte(hiddenReasoning)) {
		t.Fatal("test did not exercise a persisted provider-only reasoning item")
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodGet, api.URL+"/api/sessions/"+id+"/files?path=evidence.txt", nil)
	var file struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &file); err != nil || status != http.StatusOK || file.Content != evidence {
		t.Fatalf("generated file read: status=%d body=%s err=%v", status, body, err)
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodGet, api.URL+"/api/sessions/"+id+"/files?path=../.env", nil)
	if status == http.StatusOK {
		t.Fatal("workspace traversal accepted by HTTP file reader")
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodGet, api.URL+"/api/sessions", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(id)) {
		t.Fatalf("session not recoverable from history: status=%d body=%s", status, body)
	}
	feedback := map[string]string{"actor": "demo-reviewer", "message": "The demo artifact passed its sandbox check."}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/tasks/"+task+"/feedback", feedback)
	if status != http.StatusAccepted || !humanDelivered.Load() {
		t.Fatalf("feedback was not journaled and delivered: status=%d body=%s", status, body)
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodGet, api.URL+"/api/tasks/"+task+"/memory", nil)
	var memories struct {
		Records []storage.Memory `json:"records"`
	}
	if err := json.Unmarshal(body, &memories); err != nil || status != http.StatusOK {
		t.Fatalf("read feedback journal: status=%d body=%s err=%v", status, body, err)
	}
	humanRecords := 0
	for _, record := range memories.Records {
		if record.Source == "human:demo-reviewer" && record.Content == feedback["message"] {
			humanRecords++
		}
	}
	if humanRecords != 1 || retained.Load() < 2 {
		t.Fatalf("feedback attribution missing: records=%+v retained=%d", memories.Records, retained.Load())
	}
	status, body = integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/tasks/"+task+"/recall", map[string]string{"query": "Which approach has evidence of working?"})
	var recall struct {
		Facts []memory.Fact `json:"facts"`
	}
	if err := json.Unmarshal(body, &recall); err != nil || status != http.StatusOK || len(recall.Facts) != 2 {
		t.Fatalf("recall feedback evidence: status=%d body=%s err=%v", status, body, err)
	}
	if recall.Facts[0].Provenance != "attributed_fact" || recall.Facts[0].Metadata["source"] != "human:demo-reviewer" || recall.Facts[1].Provenance != "derived_observation_verify_sources" || len(recall.Facts[1].Sources) != 1 || recall.Facts[1].Sources[0].Metadata["source"] != "human:demo-reviewer" {
		t.Fatalf("recall flattened human and derived provenance: %+v", recall.Facts)
	}
	// Retry after completion must still reuse the same execution and paid turns.
	status, body = integrationRequest(t, ctx, httpClient, http.MethodPost, api.URL+"/api/sessions", request)
	if status != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("completed session retry duplicated work: calls=%d status=%d body=%s", calls.Load(), status, body)
	}
	originBody, _ := json.Marshal(map[string]any{"id": "evil-" + rand.Text(), "task": task, "prompt": "Cross-origin request must not launch work."})
	originRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/api/sessions", bytes.NewReader(originBody))
	if err != nil {
		t.Fatal(err)
	}
	originRequest.Header.Set("Content-Type", "application/json")
	originRequest.Header.Set("Origin", "https://untrusted.example")
	originResponse, err := httpClient.Do(originRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = originResponse.Body.Close()
	if originResponse.StatusCode != http.StatusForbidden || calls.Load() != 3 {
		t.Fatalf("cross-origin mutation accepted: status=%d calls=%d", originResponse.StatusCode, calls.Load())
	}
}
