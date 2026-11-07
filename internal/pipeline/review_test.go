package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// Run against the isolated integration fixture's real Postgres and Temporal.
// Counter windows use retained successful output; the triggering failure,
// authoring, frozen evaluation, regression replay, and subsequent serving all
// execute through their real workflows and Docker nodes.
func testAutoReviewRepair(t *testing.T, ctx context.Context, s *Service, temporal client.Client, task string, parent Release, input graph.Artifact, result execution.RunResult, calls *atomic.Int32) {
	t.Helper()
	activities := &Activities{Service: s}
	complete := func(id string) error {
		batch := BatchInput{ID: task + id, ReleaseID: parent.ID, Input: input, RowCount: 5000}
		if _, _, err := s.AdmitBatch(ctx, batch); err != nil {
			return err
		}
		return activities.PipelineBatchFinish(ctx, BatchFinish{ID: batch.ID, Result: result})
	}
	initialCalls := calls.Load()
	if err := complete("-before-enable"); err != nil {
		t.Fatal(err)
	}
	if review, err := s.ClaimReview(ctx, task); err != nil || review != nil {
		t.Fatal("disabled review claimed work", err)
	}
	if err := s.SetAutoReview(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 49; i++ {
		if err := complete(fmt.Sprintf("-healthy-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Repeated opt-in and completion retries preserve the counter.
	if err := s.SetAutoReview(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	if err := complete("-healthy-0"); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Batch(ctx, task+"-healthy-0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activities.PipelineBatchPrepare(ctx, duplicate.BatchInput); err != nil {
		t.Fatal(err)
	}
	retained, err := s.Batch(ctx, duplicate.ID)
	if err != nil || retained.Status != "completed" {
		t.Fatal("resubmission lost terminal batch status", err)
	}

	if review, err := s.ClaimReview(ctx, task); err != nil || review != nil {
		t.Fatal("review triggered before 50", err)
	}
	status, err := s.AutoReviewStatus(ctx, task)
	if err != nil || status.Finished != 49 || status.Remaining != 1 {
		t.Fatalf("counter %+v %v", status, err)
	}
	if err := complete("-healthy-49"); err != nil {
		t.Fatal(err)
	}
	// Multiple dispatchers consume each window exactly once.
	var wg sync.WaitGroup
	results := make(chan *AutoReview, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); review, err := s.ClaimReview(ctx, task); results <- review; failures <- err }()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	claimed := 0
	for review := range results {
		if review != nil {
			claimed++
			if review.Status != "no_action" {
				t.Fatalf("healthy window authored: %+v", review)
			}
		}
	}
	if claimed != 1 || calls.Load() != initialCalls {
		t.Fatal("healthy window called model or consumed twice", claimed)
	}
	// Concurrent completions and claims cannot skip a late-committing signal.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := complete(fmt.Sprintf("-concurrent-%d", i)); err != nil {
				t.Error(err)
			}
			if _, err := s.ClaimReview(ctx, task); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, err = s.ClaimReview(ctx, task); err != nil {
		t.Fatal(err)
	}
	status, err = s.AutoReviewStatus(ctx, task)
	if err != nil || status.Finished != 0 {
		t.Fatalf("lost concurrent completions: %+v %v", status, err)
	}
	for i := 0; i < 49; i++ {
		if err := complete(fmt.Sprintf("-repair-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	brokenInput, rows, err := s.Datasets.InferenceInput(ctx, parent.DatasetID, strings.NewReader("signal,noise\n,1\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	broken := BatchInput{ID: task + "-null-failure", ReleaseID: parent.ID, Input: brokenInput, RowCount: rows}
	if _, _, err = s.AdmitBatch(ctx, broken); err != nil {
		t.Fatal(err)
	}
	run, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: broken.ID, TaskQueue: task}, BatchWorkflow, broken)
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Get(ctx, nil); err == nil {
		t.Fatal("expected null-handling failure")
	}
	batch, err := s.Batch(ctx, broken.ID)
	if err != nil || batch.Status != "failed" || len(batch.Trace) == 0 || batch.Trace[0].ID != "prepare" {
		t.Fatalf("lost failed node trace: %+v %v", batch, err)
	}
	if calls.Load() != initialCalls {
		t.Fatal("serving called authoring model")
	}
	review, err := s.ClaimReview(ctx, task)
	if err != nil || review == nil {
		t.Fatal("no review at 50", err)
	}
	if review.Status != "queued" || review.Build.NodeID != "serve:prepare" || review.Build.MaxTurns != 12 || review.Build.Regression.BatchID != broken.ID || review.Missing["signal"] != 1 {
		t.Fatalf("invalid review: %+v", review)
	}
	if strings.Contains(review.Summary, "Traceback") || strings.Contains(review.Summary, "row_ids") {
		t.Fatal("raw diagnostics in review prompt")
	}
	if err = s.DispatchAutoReviews(ctx, task); err != nil {
		t.Fatal(err)
	}
	var candidateID string
	if err = temporal.GetWorkflow(ctx, "internal/review/"+review.ID, "").Get(ctx, &candidateID); err != nil {
		t.Fatal("automatic repair failed", err)
	}
	candidate, err := s.Release(ctx, candidateID)
	if err != nil || candidate.ApprovedAt != nil || candidate.Regression == nil || !candidate.Regression.Passed || candidate.Status != "eligible" || candidate.FitArtifact != parent.FitArtifact {
		t.Fatalf("invalid repaired candidate: %+v %v", candidate, err)
	}
	if err = execution.ValidateTuning(parent.Graph, candidate.Graph, "serve:prepare"); err != nil {
		t.Fatal(err)
	}
	if candidate.Development.Metrics.F1 != parent.Development.Metrics.F1 || candidate.Audit.Metrics.F1 != parent.Audit.Metrics.F1 {
		t.Fatal("repair changed evaluation performance")
	}
	active, err := s.Active(ctx, task)
	if err != nil || active != parent.ID {
		t.Fatal("automatic review activated candidate", err)
	}
	stored, err := s.Review(ctx, review.ID)
	if err != nil || stored.Status != "awaiting_approval" || stored.CandidateID != candidateID {
		t.Fatalf("review state %+v %v", stored, err)
	}
	if calls.Load() != initialCalls+2 {
		t.Fatal("unexpected repair model calls", calls.Load()-initialCalls)
	}
	if _, _, err = s.AdmitBatch(ctx, BatchInput{ID: task + "-unauthorized-repair", ReleaseID: candidateID, Input: brokenInput, RowCount: rows}); err == nil {
		t.Fatal("repair bypassed human approval")
	}
	for i := 0; i < 50; i++ {
		if err := complete(fmt.Sprintf("-pending-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Cooldown applies even after an unsuccessful attempt.
	if _, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status='failed' WHERE id=$1`, review.ID); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimReview(ctx, task); err != nil || next != nil {
		t.Fatal("cooldown admitted another attempt", err)
	}
	if _, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status='awaiting_approval' WHERE id=$1`, review.ID); err != nil {
		t.Fatal(err)
	}
	// Exceeding the cooldown must still leave only one pending proposal.
	if _, err = s.DB.Pool.Exec(ctx, `UPDATE graph_review_settings SET next_allowed_at=now()-interval '1 minute' WHERE task_id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimReview(ctx, task); err != nil || next != nil {
		t.Fatal("pending candidate allowed duplicate review", err)
	}
	// Approval is performed by an isolated fixture human, using only its temp key.
	key, err := ReadApprovalKey(s.Files.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, candidateID, "fixture-human-reviewer", key, parent.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Review(ctx, review.ID)
	if err != nil || stored.Status != "approved" {
		t.Fatalf("approval outcome %+v %v", stored, err)
	}
	status, err = s.AutoReviewStatus(ctx, task)
	if err != nil || status.Finished != 0 {
		t.Fatal("new release inherited old batch count", err)
	}
	fixed := BatchInput{ID: task + "-fixed-batch", ReleaseID: candidateID, Input: brokenInput, RowCount: rows}
	if _, _, err = s.AdmitBatch(ctx, fixed); err != nil {
		t.Fatal(err)
	}
	run, err = temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: fixed.ID, TaskQueue: task}, BatchWorkflow, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Get(ctx, nil); err != nil {
		t.Fatal("approved repair failed serving", err)
	}
	if calls.Load() != initialCalls+2 {
		t.Fatal("repaired serving invoked model")
	}
	if err = s.SetAutoReview(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAutoReview(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	status, err = s.AutoReviewStatus(ctx, task)
	if err != nil || status.Finished != 0 {
		t.Fatal("reenable did not reset baseline", err)
	}
	// Reconciliation recovers a terminal workflow whose finish write was lost.
	if _, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status='running',payload=jsonb_set(payload,'{status}','"running"') WHERE id=$1`, review.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileAutoReviews(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Review(ctx, review.ID)
	if err != nil || stored.Status != "approved" {
		t.Fatal("reconciliation lost approved decision", err)
	}
	// Replay both ordinary pre-review builds and the new repair histories.
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(BuildWorkflow)
	replayer.RegisterWorkflow(BatchWorkflow)
	replayer.RegisterWorkflow(AutoReviewWorkflow)
	for _, id := range []string{task + "-build", broken.ID, review.ID, "internal/review/" + review.ID, fixed.ID} {
		history := &historypb.History{}
		iterator := temporal.GetWorkflowHistory(ctx, id, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for iterator.HasNext() {
			event, err := iterator.Next()
			if err != nil {
				t.Fatal(err)
			}
			history.Events = append(history.Events, event)
		}
		if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
			t.Fatalf("history %s failed replay: %v", id, err)
		}
	}

	var memories int
	if err = s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_memory WHERE dedup_key IN ($1,$2,$3)`, review.ID+"/trigger", review.ID+"/outcome", review.ID+"/approval").Scan(&memories); err != nil || memories != 3 {
		t.Fatal("missing memory provenance", memories, err)
	}
}

func TestAutoReviewCleansUpPrepareFailuresAndDisabledWork(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			suite := testsuite.WorkflowTestSuite{}
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivityWithOptions(func(context.Context, string) (ReviewWork, error) {
				if fail {
					return ReviewWork{}, fmt.Errorf("fixture prepare failure")
				}
				return ReviewWork{}, nil
			}, activity.RegisterOptions{Name: "PipelineAutoReviewPrepare"})
			finished := false
			env.RegisterActivityWithOptions(func(_ context.Context, in ReviewFinish) error {
				finished = true
				if in.ID != "review-fixture" || in.Failed != fail || in.CandidateID != "" {
					t.Errorf("incorrect cleanup %+v", in)
				}
				return nil
			}, activity.RegisterOptions{Name: "PipelineAutoReviewFinish"})
			env.ExecuteWorkflow(AutoReviewWorkflow, "review-fixture")
			if !finished || (env.GetWorkflowError() != nil) != fail {
				t.Fatal("review cleanup lost terminal outcome", env.GetWorkflowError())
			}
		})
	}
}
