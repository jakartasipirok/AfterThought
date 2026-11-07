package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/model"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func mockPolicyResponse(messages []model.Message) (model.Message, string) {
	var doc PolicyDocument
	var signals []Novelty
	readPolicy, readEvidence, written := false, false, false
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		var result struct {
			Output string `json:"output"`
		}
		_ = json.Unmarshal([]byte(message.Content), &result)
		switch message.ToolCallID {
		case "read-policy":
			_ = json.Unmarshal([]byte(result.Output), &doc)
			readPolicy = true
		case "read-evidence":
			_ = json.Unmarshal([]byte(result.Output), &signals)
			readEvidence = true
		case "write-proposal":
			written = true
		}
	}
	message := model.Message{Role: "assistant", Content: "The count departure could reflect reuse or a data issue. Investigators should check provenance before any claim decision."}
	if written {
		return message, "stop"
	}
	if !readPolicy || !readEvidence {
		message.ToolCalls = []model.Call{
			{ID: "read-policy", Type: "function", Function: model.FunctionCall{Name: "read", Arguments: `{"path":"knowledge/company-policy.json"}`}},
			{ID: "read-evidence", Type: "function", Function: model.FunctionCall{Name: "read", Arguments: `{"path":"pattern-evidence.json"}`}},
		}
	} else {
		draft := PolicyDraft{Title: "Check repeated evidence", Hypothesis: "Possible evidence reuse; unconfirmed", Rationale: "Counts depart from the training range.", Uncertainty: "A changed intake process or data duplication could explain this. Verify source records.", Amendment: "Investigators should verify original evidence provenance and document any benign explanation before escalating a claim.", Citations: []PolicyCitation{{Page: doc.Pages[0].Number, Quote: "Retain the original documents"}}, EvidenceIDs: []string{signals[0].ID}}
		message.ToolCalls = []model.Call{{ID: "write-proposal", Type: "function", Function: model.FunctionCall{Name: "write", Arguments: string(encode(map[string]string{"path": "policy-proposal.json", "content": string(encode(draft))}))}}}
	}
	return message, "tool_calls"
}

func testPolicyReview(t *testing.T, ctx context.Context, s *Service, temporal client.Client, task string, calls *atomic.Int32) {
	t.Helper()
	active, err := s.Active(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.Release(ctx, active)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAutoReview(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	expireCooldown := func() {
		t.Helper()
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE graph_review_settings SET next_allowed_at=now()-interval '1 minute' WHERE task_id=$1`, task); err != nil {
			t.Fatal(err)
		}
	}
	expireCooldown()
	runBatch := func(suffix, noise string) {
		t.Helper()
		csv := "signal,noise\n" + strings.Repeat("0,"+noise+"\n1,"+noise+"\n", 10)
		input, rows, err := s.Datasets.InferenceInput(ctx, parent.DatasetID, strings.NewReader(csv))
		if err != nil {
			t.Fatal(err)
		}
		in := BatchInput{ID: task + suffix, ReleaseID: active, Input: input, RowCount: rows}
		if _, _, err = s.AdmitBatch(ctx, in); err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		run, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: in.ID, TaskQueue: task}, BatchWorkflow, in)
		if err != nil {
			t.Fatal(err)
		}
		var result execution.RunResult
		if err = run.Get(ctx, &result); err != nil {
			t.Fatal(err)
		}
		if before != calls.Load() {
			t.Fatal("novelty collection called model")
		}
	}
	runBatch("-novel", "20")
	if r, err := s.ClaimReview(ctx, task); err != nil || r != nil {
		t.Fatal("disabled auto review ran", err)
	}
	review, err := s.ReviewNow(ctx, task)
	if err != nil || review == nil || review.Kind != "emerging_pattern" || review.Status != "needs_policy" || !review.Manual {
		t.Fatalf("manual review %+v %v", review, err)
	}
	if _, err = s.DB.Session(ctx, review.SessionID); err == nil {
		t.Fatal("no-policy review used model")
	}
	raw, err := os.ReadFile("../../frontend/assets/demo-company-policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.UploadPolicy(ctx, task, "Fictional test policy", "fixture-uploader", true, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.QueuePolicyDraft(ctx, review.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchAutoReviews(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = temporal.GetWorkflow(ctx, "internal/review/"+review.ID, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	proposal, err := s.PolicyProposal(ctx, review.ID)
	if err != nil || proposal.Status != "awaiting_policy_review" {
		t.Fatalf("proposal %+v %v", proposal, err)
	}
	status, err := s.AutoReviewStatus(ctx, task)
	if err != nil || status.Enabled || status.Latest.Status != "awaiting_policy_review" {
		t.Fatal("manual review enabled cadence or lost proposal", err)
	}
	if _, err = s.DecidePolicy(ctx, review.ID, "approve", "suspected_scam", "Evidence needs checking", "fixture-human", "invalid", doc.ID); !errors.Is(err, ErrApproval) {
		t.Fatal("accepted invalid key", err)
	}
	// Only the isolated fixture key is read; the live host key is never used.
	key, err := ReadApprovalKey(s.Files.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecidePolicy(ctx, review.ID, "approve", "suspected_scam", "Evidence needs checking", "fixture-human", key, "stale"); err == nil {
		t.Fatal("accepted stale policy comparison")
	}
	current, err := s.CurrentPolicy(ctx, task)
	if err != nil || current.ID != doc.ID {
		t.Fatal("policy published before human approval", err)
	}
	decided, err := s.DecidePolicy(ctx, review.ID, "approve", "needs_investigation", "Investigate originals before any claim consequence", "fixture-human", key, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = s.CurrentPolicy(ctx, task)
	if err != nil || current.Version != 2 || current.ID != decided.PublishedPolicyID || current.ParentID != doc.ID {
		t.Fatal("versioned amendment lost", err)
	}
	if got, err := s.Active(ctx, task); err != nil || got != active {
		t.Fatal("policy publication activated graph", err)
	}
	if _, err = s.DecidePolicy(ctx, review.ID, "approve", "suspected_scam", "retry", "fixture-human", key, current.ID); err == nil {
		t.Fatal("repeated decision accepted")
	}
	if _, err = s.UploadPolicy(ctx, task, "Duplicate", "fixture-uploader", true, raw); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CurrentPolicy(ctx, task); err != nil || got.ID != current.ID {
		t.Fatal("duplicate upload reverted amendment", err)
	}
	workspace := t.TempDir()
	note, err := s.PrepareAuthoring(ctx, agentruntime.Input{Task: task, PolicyID: current.ID}, workspace)
	if err != nil || !strings.Contains(note, "version 2") {
		t.Fatal("policy not staged", err)
	}
	expireCooldown()
	runBatch("-repeat-pattern", "20")
	before := calls.Load()
	r, err := s.ReviewNow(ctx, task)
	if err != nil || r == nil || r.Status != "no_action" || before != calls.Load() {
		t.Fatalf("human-reviewed signal was re-proposed %+v %v", r, err)
	}
	// A different direction creates new evidence and can be explicitly dismissed.
	runBatch("-different-pattern", "-20")
	r, err = s.ReviewNow(ctx, task)
	if err != nil || r == nil || r.Status != "queued" {
		t.Fatalf("new signal %+v %v", r, err)
	}
	if err = s.DispatchAutoReviews(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = temporal.GetWorkflow(ctx, "internal/review/"+r.ID, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecidePolicy(ctx, r.ID, "dismiss", "benign_change", "Test fixture ingestion change", "fixture-human", key, current.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CurrentPolicy(ctx, task); err != nil || got.ID != current.ID {
		t.Fatal("dismiss changed policy", err)
	}
	var memories int
	if err = s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_memory WHERE source='human:fixture-human' AND dedup_key IN ($1,$2)`, review.ID+"/policy-decision", r.ID+"/policy-decision").Scan(&memories); err != nil || memories != 2 {
		t.Fatal("missing attributed decisions", err)
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(PolicyReviewWorkflow)
	history := &historypb.History{}
	iterator := temporal.GetWorkflowHistory(ctx, "internal/review/"+review.ID, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, event)
	}
	if err = replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal("policy workflow replay", err)
	}
}
