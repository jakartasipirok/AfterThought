package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/model"
	"github.com/jakartasipirok/AfterThought/internal/pipeline"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func startWorker(ctx context.Context, db *storage.DB, files *storage.Files, c client.Client, queue, modelName string) (worker.Worker, error) {
	if modelName == "" {
		return nil, fmt.Errorf("set AFTERTHOUGHT_MODEL in .env or pass --model")
	}
	image, err := harness.ResolveImage(ctx, env("AFTERTHOUGHT_SANDBOX_IMAGE", "python:3.12-bookworm"))
	if err != nil {
		return nil, err
	}
	skills, err := filepath.Abs(env("AFTERTHOUGHT_SKILLS_DIR", "harness/skills"))
	if err != nil {
		return nil, err
	}
	if _, err = harness.Catalog(skills); err != nil {
		return nil, err
	}
	h, err := memoryConfig()
	if err != nil {
		return nil, err
	}
	a := &agentruntime.Activities{
		DB: db, Files: files, Skills: skills, Image: image, Hindsight: h,
		Network: env("AFTERTHOUGHT_SANDBOX_NETWORK", "none"),
		Model:   &model.Client{Model: modelName, API: env("AFTERTHOUGHT_MODEL_API", "auto"), BaseURL: env("AFTERTHOUGHT_MODEL_BASE_URL", "https://api.openai.com/v1"), APIKey: os.Getenv("AFTERTHOUGHT_MODEL_API_KEY")},
	}
	s, err := newPipeline(ctx, db, files)
	if err != nil {
		return nil, err
	}
	a.GraphImage = s.Engine.Image
	a.Prepare = s.PrepareAuthoring
	w := worker.New(c, queue, worker.Options{MaxConcurrentActivityExecutionSize: 4})
	w.RegisterWorkflow(agentruntime.AgentWorkflow)
	w.RegisterActivity(a)
	w.RegisterWorkflow(execution.GraphWorkflow)
	w.RegisterActivity(&execution.Activities{Engine: s.Engine})
	w.RegisterWorkflow(pipeline.BuildWorkflow)
	w.RegisterWorkflow(pipeline.BatchWorkflow)
	w.RegisterActivity(&pipeline.Activities{Service: s})
	if err = w.Start(); err != nil {
		return nil, err
	}
	return w, nil
}

func reapContainers(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		harness.Reap(cleanup)
		execution.ReapExpired(cleanup)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
