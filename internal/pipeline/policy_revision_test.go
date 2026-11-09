package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/graph"
)

func TestTriageRevisionRejectsOtherFailures(t *testing.T) {
	r := Release{Status: "rejected", Development: dataset.Report{PredictionArtifact: graph.Artifact{Digest: "saved"}, Failures: []string{"accuracy is below the held-out majority baseline"}}, Audit: dataset.Report{PredictionArtifact: graph.Artifact{Digest: "saved"}}}
	if err := triageRevisionAllowed(r); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Release){
		func(r *Release) { r.Status = "eligible" },
		func(r *Release) { r.ExecutionError = "failed" },
		func(r *Release) { r.Audit.Failures = []string{"recall is below the frozen minimum"} },
		func(r *Release) {
			r.Development.Failures = []string{"development F1 regressed against the immutable incumbent"}
		},
		func(r *Release) { r.Audit.PredictionArtifact = graph.Artifact{} },
		func(r *Release) { r.AutoReviewID = "automatic" },
	} {
		copy := r
		mutate(&copy)
		if err := triageRevisionAllowed(copy); err == nil {
			t.Fatal("unrelated failure was waived")
		}
	}
}

func TestIntegrationTriageRevisionPreservesEvidenceAndApproval(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires local services; no model calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, _ := pipelineFixtureStore(t, ctx)
	var csv bytes.Buffer
	csv.WriteString("signal,label\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&csv, "%d,%d\n", i, i%10/9)
	}
	d, err := s.Datasets.Create(ctx, &csv, dataset.CreateOptions{Name: "Triage fixture", Task: "triage-fixture", Target: "label", PositiveLabel: "1", LabelMeaning: "Synthetic test label", FeatureColumns: []string{"signal"}, LabelsVerified: true, Synthetic: true, MinPrecision: .1, MinRecall: .15, MaxFalsePositiveRate: .4, Threshold: .5})
	if err != nil {
		t.Fatal(err)
	}
	r := Release{ID: "triage-parent", Task: d.Task, DatasetID: d.ID, Status: "rejected", CreatedAt: time.Now().UTC()}
	for _, split := range []string{"development", "audit"} {
		b, err := s.Files.Get(d.Splits[split].Labeled)
		if err != nil {
			t.Fatal(err)
		}
		var truth dataset.Data
		if err = json.Unmarshal(b, &truth); err != nil {
			t.Fatal(err)
		}
		output := dataset.Output{}
		positive, negative := 0, 0
		for i, id := range truth.RowIDs {
			p, decision := .1, "accept"
			if truth.Labels[i] == 1 {
				positive++
			} else {
				negative++
			}
			if (truth.Labels[i] == 1 && positive == 1) || (truth.Labels[i] == 0 && negative <= 2) {
				p, decision = .9, "escalate"
			}
			output.Predictions = append(output.Predictions, dataset.Prediction{RowID: id, Probability: p, Decision: decision, Reasons: []string{"fixture"}})
		}
		artifact, err := s.Files.Put(encode(output))
		if err != nil {
			t.Fatal(err)
		}
		report, err := s.Datasets.Evaluate(ctx, d.ID, split, artifact)
		if err != nil || report.Passed {
			t.Fatalf("fixture must fail accuracy: %+v %v", report, err)
		}
		if split == "development" {
			r.Development = report
		} else {
			r.Audit = report
		}
	}
	if err = s.SaveRelease(ctx, r); err != nil {
		t.Fatal(err)
	}
	key, err := ReadApprovalKey(s.Files.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequalifyTriage(ctx, r.ID, "operator", "invalid", "Fraud triage measures review utility instead of overall accuracy."); !errors.Is(err, ErrApproval) {
		t.Fatal("unauthorized revision accepted", err)
	}
	revised, err := s.RequalifyTriage(ctx, r.ID, "operator", key, "Fraud triage measures review utility instead of overall accuracy.")
	if err != nil {
		t.Fatal(err)
	}
	if revised.ID == r.ID || revised.Status != "eligible" || revised.ApprovedAt != nil || revised.Development.Metrics != r.Development.Metrics || revised.Audit.Metrics != r.Audit.Metrics {
		t.Fatal("revision changed evidence or approved itself")
	}
	if err = s.Approve(ctx, r.ID, "operator", key, ""); err == nil {
		t.Fatal("original rejected release became approvable")
	}
	if err = s.Approve(ctx, revised.ID, "operator", key, "stale"); err == nil {
		t.Fatal("stale activation accepted")
	}
	if err = s.Approve(ctx, revised.ID, "operator", key, ""); err != nil {
		t.Fatal(err)
	}
	original, err := s.Release(ctx, r.ID)
	if err != nil || !bytes.Equal(encode(original), encode(r)) {
		t.Fatal("original rejection was overwritten", err)
	}
	active, err := s.Active(ctx, r.Task)
	if err != nil || active != revised.ID {
		t.Fatal("revised release not active", err)
	}
}
