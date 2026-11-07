package pipeline

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/model"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func pipelineTestEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func pipelineFixtureStore(t *testing.T, ctx context.Context) (*Service, client.Client) {
	t.Helper()
	dsn := pipelineTestEnv("DATABASE_URL", "postgres://afterthought:afterthought@127.0.0.1:55432/afterthought?sslmode=disable")
	admin, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Pool.Close)
	schema := "pipeline_test_" + strings.ToLower(rand.Text())
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
	if err = os.MkdirAll("../../.afterthought", 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("../../.afterthought", "pipeline-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	files, err := storage.NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	image, err := harness.ResolveImage(ctx, pipelineTestEnv("AFTERTHOUGHT_ML_IMAGE", "afterthought-ml:v1"))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: db, Files: files, Datasets: &dataset.Store{DB: db, Files: files}, Engine: &execution.Engine{Files: files, Image: image}}
	if err = service.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	temporal, err := client.Dial(client.Options{HostPort: pipelineTestEnv("TEMPORAL_ADDRESS", "127.0.0.1:7233"), Namespace: pipelineTestEnv("TEMPORAL_NAMESPACE", "default")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(temporal.Close)
	service.Temporal = temporal
	return service, temporal
}

// These generic test nodes are supplied only by the mock author during this
// test. The production harness still starts without any domain implementations.
func pipelineAuthorFixture() (execution.Draft, map[string]string) {
	files := map[string]string{
		"nodes/fit.py": `def run(inputs, config):
    data = inputs['training']
    assert len(data['rows']) == len(data['labels'])
    positive = [float(row['signal']) for row, y in zip(data['rows'], data['labels']) if y == 1]
    negative = [float(row['signal']) for row, y in zip(data['rows'], data['labels']) if y == 0]
    split = (sum(positive) / len(positive) + sum(negative) / len(negative)) / 2
    return {'split': split, 'training_rows': len(data['rows'])}
`,
		"nodes/prepare.py": `def run(inputs, config):
    data, fitted = inputs['data'], inputs['fitted']
    assert 'labels' not in data and 'target' not in data
    scores = [0.9 if float(row['signal']) >= fitted['split'] else 0.1 for row in data['rows']]
    return {'row_ids': data['row_ids'], 'scores': scores}
`,
		"nodes/score.py": `def run(inputs, config):
    data = inputs['prepared']
    return {'predictions': [
        {'row_id': row_id, 'probability': probability,
         'decision': 'escalate' if probability >= config['threshold'] else 'accept',
         'reasons': ['Synthetic fixture score %.1f; routing threshold %.1f.' % (probability, config['threshold'])]}
        for row_id, probability in zip(data['row_ids'], data['scores'])]}
`,
	}
	draft := execution.Draft{Version: "v2", Name: "synthetic-binary-test", Fit: execution.DraftGraph{Output: "fit", Nodes: []execution.DraftNode{{ID: "fit", Source: "nodes/fit.py", Needs: []string{}, Inputs: map[string]string{"training": "$input"}, Config: json.RawMessage(`{}`), TimeoutSeconds: 30, MaxAttempts: 1}}}, Serve: execution.DraftGraph{Output: "score", Nodes: []execution.DraftNode{
		{ID: "prepare", Source: "nodes/prepare.py", Needs: []string{}, Inputs: map[string]string{"data": "$input", "fitted": "$fit"}, Config: json.RawMessage(`{}`), TimeoutSeconds: 30, MaxAttempts: 1},
		{ID: "score", Source: "nodes/score.py", Needs: []string{"prepare"}, Inputs: map[string]string{"prepared": "prepare"}, Config: json.RawMessage(`{"threshold":0.5}`), TimeoutSeconds: 30, MaxAttempts: 1},
	}}}
	return draft, files
}

func TestIntegrationAuthorEvaluateApproveServe5000AndTuneOneNode(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires Docker, Postgres, Temporal, and the local ML image; model endpoint is mocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	service, temporal := pipelineFixtureStore(t, ctx)
	draft, sources := pipelineAuthorFixture()
	approvalKey, err := os.ReadFile(ApprovalKeyPath(service.Files.Root))
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var request struct {
			Messages []model.Message `json:"messages"`
			Tools    []model.Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 4 {
			t.Error("pipeline expanded the four model-visible tools")
		}
		tuning, repairing, toolDone, policyReview := false, false, false, false
		for _, message := range request.Messages {
			if strings.Contains(message.Content, strings.TrimSpace(string(approvalKey))) {
				t.Error("host approval key was exposed to the author")
			}
			policyReview = policyReview || strings.Contains(message.Content, "policy-review.md")
			tuning = tuning || strings.Contains(message.Content, "Improve ONLY node serve:score")
			repairing = repairing || strings.Contains(message.Content, "Improve ONLY node serve:prepare")
			toolDone = toolDone || message.Role == "tool"
		}
		message := model.Message{Role: "assistant", Content: "Candidate files are ready for trusted execution and evaluation; no approval was performed."}
		finish := "stop"
		if policyReview {
			message, finish = mockPolicyResponse(request.Messages)
		} else if !toolDone {
			finish = "tool_calls"
			message.Content = ""
			if repairing {
				message.ToolCalls = []model.Call{{ID: "repair-null", Type: "function", Function: model.FunctionCall{Name: "edit", Arguments: string(encode(map[string]string{"path": "nodes/serve-prepare.py", "old_text": "float(row['signal'])", "new_text": "float(row['signal'] or 0)"}))}}}
			} else if tuning {
				message.ToolCalls = []model.Call{{ID: "tune-score-config", Type: "function", Function: model.FunctionCall{Name: "edit", Arguments: string(encode(map[string]string{"path": "candidate.json", "old_text": `"threshold": 0.5`, "new_text": `"threshold": 0.6`}))}}}
			} else {
				for _, name := range []string{"nodes/fit.py", "nodes/prepare.py", "nodes/score.py"} {
					message.ToolCalls = append(message.ToolCalls, model.Call{ID: "write-" + filepath.Base(name), Type: "function", Function: model.FunctionCall{Name: "write", Arguments: string(encode(map[string]string{"path": name, "content": sources[name]}))}})
				}
				message.ToolCalls = append(message.ToolCalls, model.Call{ID: "write-candidate", Type: "function", Function: model.FunctionCall{Name: "write", Arguments: string(encode(map[string]string{"path": "candidate.json", "content": string(encode(draft))}))}})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
	}))
	t.Cleanup(provider.Close)
	queue := "pipeline-test-" + strings.ToLower(rand.Text())
	skills, err := filepath.Abs("../../harness/skills")
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(temporal, queue, worker.Options{})
	w.RegisterWorkflow(BuildWorkflow)
	w.RegisterWorkflow(BatchWorkflow)
	w.RegisterWorkflow(AutoReviewWorkflow)
	w.RegisterWorkflow(PolicyReviewWorkflow)
	w.RegisterWorkflow(agentruntime.AgentWorkflow)
	w.RegisterWorkflow(execution.GraphWorkflow)
	w.RegisterActivity(&Activities{Service: service})
	w.RegisterActivity(&execution.Activities{Engine: service.Engine})
	w.RegisterActivity(&agentruntime.Activities{DB: service.DB, Files: service.Files, Model: &model.Client{BaseURL: provider.URL, Model: "mock"}, Skills: skills, Image: service.Engine.Image, GraphImage: service.Engine.Image, Prepare: service.PrepareAuthoring})
	if err = w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	var csvInput bytes.Buffer
	csvWriter := csv.NewWriter(&csvInput)
	_ = csvWriter.Write([]string{"signal", "noise", "outcome"})
	for i := 0; i < 5000; i++ {
		label := "no"
		if i%2 == 1 {
			label = "yes"
		}
		_ = csvWriter.Write([]string{strconv.Itoa(i % 2), strconv.Itoa(i % 7), label})
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		t.Fatal(err)
	}
	d, err := service.Datasets.Create(ctx, bytes.NewReader(csvInput.Bytes()), dataset.CreateOptions{Name: "Generated binary test fixture", Task: queue, Target: "outcome", PositiveLabel: "yes", LabelMeaning: "Synthetic test class equals the generated signal feature; not real fraud truth.", FeatureColumns: []string{"signal", "noise"}, LabelsVerified: true, Synthetic: true, MinPrecision: .99, MinRecall: .99, MaxFalsePositiveRate: .01, Threshold: .5})
	if err != nil {
		t.Fatal(err)
	}
	build := BuildInput{ID: queue + "-build", Task: queue, DatasetID: d.ID, Prompt: "Build the generic synthetic binary test pipeline.", MaxTurns: 3}
	// Before internal child IDs were namespaced, this public session ID could
	// collide with a build's author child. It belongs to an unrelated request.
	foreignTranscript, err := service.Files.Put([]byte(`[{"role":"user","content":"An unrelated legacy session; never reuse this context."}]`))
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, err := service.DB.CreateSession(ctx, storage.Session{ID: build.ID + "-author", Task: "unrelated-task", Transcript: foreignTranscript, Image: service.Engine.Image, InputDigest: graph.Hash([]byte("unrelated-request"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := service.AdmitBuild(ctx, build); err != nil || !created {
		t.Fatal("could not admit fixture build", err)
	}
	run, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: build.ID, TaskQueue: queue}, BuildWorkflow, build)
	if err != nil {
		t.Fatal(err)
	}
	var releaseID string
	if err = run.Get(ctx, &releaseID); err != nil {
		t.Fatal("full authoring/evaluation workflow failed", err)
	}
	release, err := service.Release(ctx, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	if release.Status != "eligible" || release.ApprovedAt != nil || !release.Development.Passed || !release.Audit.Passed || release.Development.Metrics.Accuracy != 1 || release.Audit.Metrics.Accuracy != 1 || release.Development.Metrics.Total != 1000 || release.Audit.Metrics.Total != 1000 || len(release.FitTrace) != 1 || len(release.DevelopmentTrace) != 2 || len(release.AuditTrace) != 2 {
		t.Fatalf("incorrect executable/evaluated release: %+v", release)
	}
	fitBytes, err := service.Files.Get(release.FitArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var fitted struct {
		Rows  int     `json:"training_rows"`
		Split float64 `json:"split"`
	}
	if err = json.Unmarshal(fitBytes, &fitted); err != nil || fitted.Rows != 3000 || fitted.Split != .5 {
		t.Fatalf("fit did not train on the frozen training split: %+v %v", fitted, err)
	}
	if modelCalls.Load() != 2 {
		t.Fatalf("unexpected authoring model calls: %d", modelCalls.Load())
	}
	job, err := service.Job(ctx, build.ID)
	if err != nil || job.SessionID != AuthorSessionID(build.ID, 0) || job.SessionID == foreignSession.ID {
		t.Fatalf("build reused a public legacy author session: %+v %v", job, err)
	}
	authorSession, err := service.DB.Session(ctx, job.SessionID)
	if err != nil || authorSession.Task != queue || authorSession.InputDigest == "" || authorSession.Transcript == foreignTranscript {
		t.Fatalf("namespaced author request lost identity isolation: %+v %v", authorSession, err)
	}
	unchanged, err := service.DB.Session(ctx, foreignSession.ID)
	if err != nil || graph.Digest(unchanged) != graph.Digest(foreignSession) {
		t.Fatal("build changed the unrelated legacy session", err)
	}
	input, rows, err := service.Datasets.InferenceInput(ctx, d.ID, bytes.NewReader(csvInput.Bytes()))
	if err != nil || rows != 5000 {
		t.Fatal("could not prepare 5000-row serving input", err)
	}
	batchInput := BatchInput{ID: queue + "-batch", ReleaseID: releaseID, Input: input, RowCount: rows}
	if _, _, err = service.AdmitBatch(ctx, batchInput); err == nil {
		t.Fatal("unapproved candidate was admitted for serving")
	}
	if err = service.Approve(ctx, releaseID, "fixture-human-reviewer", "incorrect-operator-code", ""); !errors.Is(err, ErrApproval) {
		t.Fatal("incorrect operator code was accepted", err)
	}
	if err = service.Approve(ctx, releaseID, "fixture-human-reviewer", string(approvalKey), ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := service.AdmitBatch(ctx, batchInput); err != nil || !created {
		t.Fatal("approved serving batch was not admitted", err)
	}
	beforeServing := modelCalls.Load()
	batchRun, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: batchInput.ID, TaskQueue: queue}, BatchWorkflow, batchInput)
	if err != nil {
		t.Fatal(err)
	}
	var result execution.RunResult
	if err = batchRun.Get(ctx, &result); err != nil {
		t.Fatal("approved 5000-row serving workflow failed", err)
	}
	batch, err := service.Batch(ctx, batchInput.ID)
	if err != nil || batch.Status != "completed" || batch.RowCount != 5000 || len(batch.Trace) != 2 || batch.ModelCalls != 0 || batch.InputTokens != 0 || batch.OutputTokens != 0 || modelCalls.Load() != beforeServing {
		t.Fatalf("serving invoked authoring or lost runtime evidence: %+v calls=%d err=%v", batch, modelCalls.Load(), err)
	}
	if err = service.Datasets.ValidateOutput(ctx, input, batch.Output); err != nil {
		t.Fatal("full serving result was invalid", err)
	}
	// Tune exactly one config. The fit result and every other node stay pinned.
	tune := BuildInput{ID: queue + "-tune", Task: queue, DatasetID: d.ID, Prompt: "Change only the routing threshold config for the test.", MaxTurns: 3, ParentReleaseID: releaseID, NodeID: "serve:score"}
	if _, created, err := service.AdmitBuild(ctx, tune); err != nil || !created {
		t.Fatal(err)
	}
	tuneRun, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: tune.ID, TaskQueue: queue}, BuildWorkflow, tune)
	if err != nil {
		t.Fatal(err)
	}
	var tunedID string
	if err = tuneRun.Get(ctx, &tunedID); err != nil {
		t.Fatal("one-node tuning workflow failed", err)
	}
	tuned, err := service.Release(ctx, tunedID)
	if err != nil {
		t.Fatal(err)
	}
	if tunedID == releaseID || tuned.FitArtifact != release.FitArtifact || tuned.ParentReleaseID != releaseID || tuned.NodeID != "serve:score" || !tuned.Development.Passed || !tuned.Audit.Passed || tuned.ApprovedAt != nil {
		t.Fatalf("node tuning lost immutable parent/fitted state or evaluation: %+v", tuned)
	}
	if _, _, err = service.AdmitBatch(ctx, BatchInput{ID: queue + "-unapproved-tuned-batch", ReleaseID: tunedID, Input: input, RowCount: rows}); err == nil {
		t.Fatal("tuned candidate inherited the incumbent's approval and was admitted for serving")
	}
	if err = execution.ValidateTuning(release.Graph, tuned.Graph, "serve:score"); err != nil {
		t.Fatal("tuning modified a protected node/interface", err)
	}
	if graph.Digest(release.Graph.Fit) != graph.Digest(tuned.Graph.Fit) || release.Graph.Serve.Nodes[0].Revision != tuned.Graph.Serve.Nodes[0].Revision || release.Graph.Serve.Nodes[1].Source != tuned.Graph.Serve.Nodes[1].Source || release.Graph.Serve.Nodes[1].Revision == tuned.Graph.Serve.Nodes[1].Revision || len(tuned.FitTrace) != 0 {
		t.Fatal("config tuning rebuilt changed other nodes, changed source, or unnecessarily refit")
	}
	if modelCalls.Load() != beforeServing+2 {
		t.Fatalf("unexpected tuning model calls: %d", modelCalls.Load())
	}
	if err = service.Approve(ctx, tunedID, "fixture-human-reviewer", string(approvalKey), ""); err == nil {
		t.Fatal("stale channel comparison was accepted")
	}
	if err = service.Approve(ctx, tunedID, "fixture-human-reviewer", string(approvalKey), releaseID); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(ctx, queue)
	if err != nil || active != tunedID {
		t.Fatal("approved tuned release was not selected for new runs", err)
	}
	t.Run("automatic robustness review", func(t *testing.T) {
		testAutoReviewRepair(t, ctx, service, temporal, queue, tuned, input, result, &modelCalls)
	})
	t.Run("emerging pattern policy review", func(t *testing.T) {
		testPolicyReview(t, ctx, service, temporal, queue, &modelCalls)
	})
}
