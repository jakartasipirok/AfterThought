package main

import (
	"context"
	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/pipeline"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

func newPipeline(ctx context.Context, db *storage.DB, files *storage.Files) (*pipeline.Service, error) {
	image, err := harness.ResolveImage(ctx, env("AFTERTHOUGHT_GRAPH_IMAGE", "afterthought-ml:v1"))
	if err != nil {
		return nil, err
	}
	h, err := memoryConfig()
	if err != nil {
		return nil, err
	}
	s := &pipeline.Service{DB: db, Files: files, Datasets: &dataset.Store{DB: db, Files: files}, Engine: &execution.Engine{Files: files, Image: image}, Hindsight: h}
	return s, s.Migrate(ctx)
}
