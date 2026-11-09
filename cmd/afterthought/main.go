package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jakartasipirok/AfterThought/internal/pipeline"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	datasetstore "github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"github.com/joho/godotenv"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func memoryConfig() (*memory.Hindsight, error) {
	backend := env("AFTERTHOUGHT_MEMORY_BACKEND", "hindsight")
	if backend == "local" {
		return nil, nil
	}
	if backend != "hindsight" {
		return nil, fmt.Errorf("memory backend must be hindsight or local")
	}
	return &memory.Hindsight{BaseURL: env("HINDSIGHT_BASE_URL", "http://127.0.0.1:8888"), APIKey: os.Getenv("HINDSIGHT_API_KEY"), BankPrefix: env("HINDSIGHT_BANK_PREFIX", "afterthought")}, nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "afterthought:", err)
		os.Exit(1)
	}
}
func run() error {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not parse .env")
	}
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: afterthought init|serve|worker|agent|approval-code|release-triage|inspect|feedback|memory|sync-memory|reflect|tools|validate [flags]")
	}
	command := os.Args[1]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	id := f.String("id", "", "session ID")
	task := f.String("task", "", "memory scope; defaults to session ID")
	prompt := f.String("prompt", "", "agent task")
	dataset := f.String("dataset", "", "optional dataset URL")
	objective := f.String("objective", "", "optional dataset objective")
	turns := f.Int("max-turns", 12, "model turn budget, 1..32")
	graphPath := f.String("graph", "graph.json", "graph manifest path")
	actor := f.String("actor", "", "feedback source identity")
	feedback := f.String("message", "", "grounded human feedback")
	approve := f.Bool("approve", false, "explicitly approve the named triage policy revision using the local host key")
	expectedCurrent := f.String("expected-current-id", "", "expected active release before triage activation; empty means none")
	modelName := f.String("model", os.Getenv("AFTERTHOUGHT_MODEL"), "model used by the worker")
	addr := f.String("addr", env("AFTERTHOUGHT_HTTP_ADDRESS", "127.0.0.1:8080"), "loopback HTTP address for serve")
	uiDir := f.String("ui-dir", env("AFTERTHOUGHT_UI_DIR", "frontend"), "frontend directory for serve")
	withWorker := f.Bool("with-worker", true, "start the agent worker with serve")
	query := f.String("query", "Which approaches worked, failed, and why? Preserve uncertainty and distinguish human feedback from model hypotheses.", "memory recall or reflection query")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if len(f.Args()) != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if command == "tools" {
		return printJSON(harness.Definitions())
	}
	if command == "approval-code" {
		path, err := filepath.Abs(pipeline.ApprovalKeyPath(env("AFTERTHOUGHT_DATA_DIR", ".afterthought")))
		if err != nil {
			return err
		}
		key, err := pipeline.ReadApprovalKey(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("approval key is unavailable; start make demo first")
		}
		_, err = fmt.Fprintln(os.Stdout, key)
		return err
	}
	if command == "validate" {
		b, err := os.ReadFile(*graphPath)
		if err != nil {
			return err
		}
		var g graph.Manifest
		if err = graph.Decode(b, &g); err != nil {
			return err
		}
		if err = g.Validate(); err != nil {
			return err
		}
		return printJSON(map[string]string{"digest": graph.Digest(g), "status": "structurally_valid", "note": "artifact availability, output schemas, and evaluation remain separate gates"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	db, err := storage.Open(ctx, env("DATABASE_URL", "postgres://afterthought:afterthought@127.0.0.1:55432/afterthought?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Pool.Close()
	files, err := storage.NewFiles(env("AFTERTHOUGHT_DATA_DIR", ".afterthought"))
	if err != nil {
		return err
	}
	switch command {
	case "release-triage":
		if *id == "" || *actor == "" || *feedback == "" {
			return fmt.Errorf("release-triage requires --id --actor --message; add --approve only with explicit operator authorization")
		}
		s := &pipeline.Service{DB: db, Files: files, Datasets: &datasetstore.Store{DB: db, Files: files}}
		key, err := pipeline.ReadApprovalKey(files.Root)
		if err != nil {
			return err
		}
		r, err := s.RequalifyTriage(ctx, *id, *actor, key, *feedback)
		if err != nil {
			return err
		}
		if *approve {
			if err = s.Approve(ctx, r.ID, *actor, key, *expectedCurrent); err != nil {
				return err
			}
			r, err = s.Release(ctx, r.ID)
			if err != nil {
				return err
			}
		}
		return printJSON(r)
	case "init":
		if err = db.Migrate(ctx); err != nil {
			return err
		}
		return printJSON(map[string]string{"status": "initialized", "domain_library": "empty", "tools": "read,bash,edit,write"})
	case "inspect":
		if *id == "" {
			return fmt.Errorf("inspect requires --id")
		}
		b, err := db.Inspect(ctx, *id)
		if err != nil {
			return err
		}
		return printJSON(b)
	case "feedback":
		if *task == "" || *actor == "" || *feedback == "" || len(*feedback) > 4000 {
			return fmt.Errorf("feedback requires --task --actor --message (up to 4000 bytes)")
		}
		if err = db.Remember(ctx, *task, "human:"+*actor, *feedback, graph.Hash([]byte(*task+"/"+*actor+"/"+*feedback))); err != nil {
			return err
		}
		h, err := memoryConfig()
		if err != nil {
			return err
		}
		if err = db.SyncMemory(ctx, h, *task); err != nil {
			return fmt.Errorf("feedback saved in outbox; Hindsight delivery pending: %w", err)
		}
		return nil
	case "sync-memory", "memory", "reflect":
		if *task == "" {
			return fmt.Errorf("memory operations require --task")
		}
		h, err := memoryConfig()
		if err != nil {
			return err
		}
		if err = db.SyncMemory(ctx, h, *task); err != nil {
			return err
		}
		if command == "sync-memory" {
			return printJSON(map[string]string{"status": "outbox_batch_submitted", "note": "Hindsight retention is asynchronous; this is not a processing-complete acknowledgement"})
		}
		if h == nil {
			if command == "reflect" {
				return fmt.Errorf("reflect requires the Hindsight backend")
			}
			recent, err := db.Memory(ctx, *task)
			if err != nil {
				return err
			}
			return printJSON(recent)
		}
		if command == "reflect" {
			result, err := h.Reflect(ctx, *task, *query)
			if err != nil {
				return err
			}
			return printJSON(result)
		}
		facts, err := h.Recall(ctx, *task, *query)
		if err != nil {
			return err
		}
		return printJSON(facts)
	}
	c, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "127.0.0.1:7233"), Namespace: env("TEMPORAL_NAMESPACE", "default")})
	if err != nil {
		return err
	}
	defer c.Close()
	queue := env("AFTERTHOUGHT_TASK_QUEUE", agentruntime.TaskQueue)
	switch command {
	case "serve":
		return serve(db, files, c, queue, *modelName, *addr, *uiDir, *withWorker)
	case "worker":
		w, err := startWorker(ctx, db, files, c, queue, *modelName)
		if err != nil {
			return err
		}
		defer w.Stop()
		workerCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		p, err := newPipeline(ctx, db, files)
		if err != nil {
			return err
		}
		p.Temporal = c
		reapContainers(workerCtx, p, queue)
		return nil
	case "agent":
		if *dataset != "" {
			problem, err := harness.NewProblem(*dataset, *objective)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(problem)
			*prompt = "Discover the task and author a graph workflow for this dataset request. Surface missing evidence before claiming predictive accuracy. Request: " + string(b) + "\n" + *prompt
		}
		if *prompt == "" {
			return fmt.Errorf("agent requires --prompt or --dataset")
		}
		if *id == "" {
			*id = "agent-" + rand.Text()
		}
		if *task == "" {
			*task = *id
		}
		if *turns < 1 || *turns > 32 {
			return fmt.Errorf("max-turns must be 1..32")
		}
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: *id, TaskQueue: queue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, agentruntime.AgentWorkflow, agentruntime.Input{ID: *id, Task: *task, Prompt: *prompt, MaxTurns: *turns})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "session:", *id)
		var out agentruntime.Outcome
		if err = run.Get(ctx, &out); err != nil {
			return err
		}
		return printJSON(out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
