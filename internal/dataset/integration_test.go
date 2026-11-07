package dataset

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

func TestIntegrationFrozen5000RowCSVAndTrustedEvaluation(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires local Postgres; no model or external service calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://afterthought:afterthought@127.0.0.1:55432/afterthought?sslmode=disable"
	}
	admin, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Close()
	schema := "dataset_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
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
	files, err := storage.NewFiles(filepath.Join(t.TempDir(), "retained"))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{DB: &storage.DB{Pool: pool}, Files: files}
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	raw := fixtureCSV(t, 5000, true)
	if len(raw) <= 1<<20 {
		t.Fatal("fixture did not exercise CSV storage beyond the envelope limit")
	}
	dataset, err := store.Create(ctx, bytes.NewReader(raw), fixtureOptions())
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Rows != 5000 || dataset.Splits["train"].Rows != 3000 || dataset.Splits["development"].Rows != 1000 || dataset.Splits["audit"].Rows != 1000 {
		t.Fatalf("incorrect stratified split counts: %+v", dataset.Splits)
	}
	again, err := store.Create(ctx, bytes.NewReader(raw), fixtureOptions())
	if err != nil || again.ID != dataset.ID || !again.CreatedAt.Equal(dataset.CreatedAt) {
		t.Fatalf("identical upload was not idempotent: %+v %v", again, err)
	}
	listed, err := store.List(ctx, dataset.Task)
	if err != nil || len(listed) != 1 || listed[0].ID != dataset.ID {
		t.Fatalf("dataset metadata did not survive retrieval: %+v %v", listed, err)
	}
	workspace := filepath.Join(t.TempDir(), "author-workspace")
	if err = store.StageTraining(ctx, dataset.ID, workspace); err != nil {
		t.Fatal(err)
	}
	if err = store.StageTraining(ctx, dataset.ID, workspace); err != nil {
		t.Fatal("stage retry failed", err)
	}
	staged := map[string]bool{}
	if err = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		staged[filepath.ToSlash(name)] = true
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, excluded := range []string{"private@example.test", "historical-disposition", "source-only-sentinel"} {
			if bytes.Contains(content, []byte(excluded)) {
				t.Errorf("source-only/PII value entered author workspace: %s", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(staged) != 3 || !staged["data/train.json"] || !staged["data/development.json"] || !staged["profile.json"] {
		t.Fatalf("author workspace must contain only train/dev data and aggregate profile: %+v", staged)
	}
	var audit Data
	ids := map[string]string{}
	for _, split := range []string{"train", "development", "audit"} {
		labeled, err := store.Inputs(ctx, dataset.ID, split, true)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := files.Get(labeled)
		if err != nil {
			t.Fatal(err)
		}
		var data Data
		if err = decodeArtifact(bytes, &data); err != nil {
			t.Fatal(err)
		}
		for _, id := range data.RowIDs {
			if prior := ids[id]; prior != "" {
				t.Fatalf("row leaked from %s to %s", prior, split)
			}
			ids[id] = split
		}
		if split == "audit" {
			audit = data
		}
		unlabeled, err := store.Inputs(ctx, dataset.ID, split, false)
		if err != nil {
			t.Fatal(err)
		}
		content, err := files.Get(unlabeled)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(content, &envelope); err != nil || len(envelope) != 2 || envelope["labels"] != nil || envelope["target"] != nil || envelope["rows"] == nil || envelope["row_ids"] == nil {
			t.Fatal("serving input contains labels or target metadata", err)
		}
	}
	var stagedTrain Data
	trainBytes, err := os.ReadFile(filepath.Join(workspace, "data", "train.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = decodeArtifact(trainBytes, &stagedTrain); err != nil {
		t.Fatal(err)
	}
	for _, id := range stagedTrain.RowIDs {
		if ids[id] == "audit" {
			t.Fatal("audit row appeared in staged training data")
		}
	}
	output := Output{Predictions: make([]Prediction, len(audit.RowIDs))}
	for i, id := range audit.RowIDs {
		probability, decision := .05, "accept"
		if audit.Labels[i] == 1 {
			probability, decision = .95, "escalate"
		}
		output.Predictions[i] = Prediction{RowID: id, Probability: probability, Decision: decision, Reasons: []string{"Synthetic test fixture score; no real fraud claim."}}
	}
	predictions, err := store.putJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.Evaluate(ctx, dataset.ID, "audit", predictions)
	if err != nil || !report.Passed || report.Metrics.Total != 1000 || report.Metrics.Accuracy != 1 || report.Metrics.MajorityBaseline != .8 || !report.Synthetic || report.PredictionArtifact != predictions {
		t.Fatalf("trusted evaluation failed: %+v %v", report, err)
	}
	for i := range output.Predictions {
		output.Predictions[i].Probability = .05
		output.Predictions[i].Decision = "accept"
	}
	majorityPredictions, err := store.putJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	majority, err := store.Evaluate(ctx, dataset.ID, "audit", majorityPredictions)
	if err != nil || majority.Passed || majority.Metrics.Accuracy != .8 || majority.Metrics.Recall != 0 {
		t.Fatalf("majority-only predictions passed meaningful gates: %+v %v", majority, err)
	}
	// Later batches may contain ignored target/PII columns. Neither is copied to
	// the serving artifact, and its immutable row identities remain deterministic.
	inferenceCSV := []byte("outcome,email,category,signal,flag\nyes,new@example.test,category-1,42,true\nno,other@example.test,category-2,3,false\n")
	inference, count, err := store.InferenceInput(ctx, dataset.ID, bytes.NewReader(inferenceCSV))
	if err != nil || count != 2 {
		t.Fatalf("later batch failed: count=%d err=%v", count, err)
	}
	content, err := files.Get(inference)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("labels")) || bytes.Contains(content, []byte("outcome")) || bytes.Contains(content, []byte("@example.test")) {
		t.Fatal("later serving batch leaked ignored target or PII columns")
	}
	sameInference, _, err := store.InferenceInput(ctx, dataset.ID, bytes.NewReader(inferenceCSV))
	if err != nil || sameInference != inference {
		t.Fatal("identical inference batch changed identity", err)
	}
	if _, _, err = store.InferenceInput(ctx, dataset.ID, strings.NewReader("signal,category,flag\nnot-a-number,category-1,true\n")); err == nil {
		t.Fatal("later batch silently changed a frozen feature type")
	}
	// A benchmark can reserve every row; those rows must never be staged for learning.
	reservedOptions := fixtureOptions()
	reservedOptions.Name = "Reserved benchmark"
	reservedOptions.EvaluationOnly = true
	reserved, err := store.Create(ctx, bytes.NewReader(raw), reservedOptions)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Splits["audit"].Rows != 5000 || reserved.Splits["train"].Rows != 0 {
		t.Fatal("benchmark did not reserve all rows")
	}
	if err = store.StageTraining(ctx, reserved.ID, t.TempDir()); err == nil {
		t.Fatal("benchmark staged for authoring")
	}
	if err = store.ValidateLearning(ctx, dataset.ID, nil, []string{reserved.ID}); err == nil {
		t.Fatal("overlapping benchmark was accepted")
	}
	if _, err = store.LearningInput(ctx, dataset.ID, []string{reserved.ID}, "train"); err == nil {
		t.Fatal("reserved data entered cumulative training")
	}
	// The earlier fixture deliberately repeats feature vectors. Learning inputs
	// require disjoint feature vectors, so give this section unique signals.
	records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(records); i++ {
		records[i][0] = strconv.Itoa(i)
	}
	var unique bytes.Buffer
	writer := csv.NewWriter(&unique)
	if err = writer.WriteAll(records); err != nil {
		t.Fatal(err)
	}
	raw = unique.Bytes()
	dataset, err = store.Create(ctx, bytes.NewReader(raw), fixtureOptions())
	if err != nil {
		t.Fatal(err)
	}
	// Change selected feature values, leaving the same schema and label semantics.
	freshRaw := bytes.ReplaceAll(raw, []byte("category-"), []byte("fresh-category-"))
	feedbackOptions := fixtureOptions()
	feedbackOptions.Name = "New verified examples"
	feedback, err := store.Create(ctx, bytes.NewReader(freshRaw), feedbackOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateLearning(ctx, dataset.ID, []string{feedback.ID}, nil); err != nil {
		t.Fatal(err)
	}
	learningWorkspace := t.TempDir()
	if err = store.StageLearning(ctx, dataset.ID, []string{feedback.ID}, learningWorkspace); err != nil {
		t.Fatal(err)
	}
	if err = store.StageLearning(ctx, dataset.ID, []string{feedback.ID}, learningWorkspace); err != nil {
		t.Fatal("learning staging retry failed", err)
	}
	mergedBytes, err := os.ReadFile(filepath.Join(learningWorkspace, "data/train.json"))
	if err != nil {
		t.Fatal(err)
	}
	var merged Data
	if err = decodeArtifact(mergedBytes, &merged); err != nil || len(merged.Rows) != 6000 {
		t.Fatal("cumulative train count", err)
	}
	feedbackAudit, err := store.labeledData(feedback, "audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range feedbackAudit.RowIDs {
		if bytes.Contains(mergedBytes, []byte(id)) {
			t.Fatal("feedback audit row entered training")
		}
	}

}
