package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type Plan struct {
	Graph       execution.Release `json:"graph"`
	Train       graph.Artifact    `json:"train"`
	Development graph.Artifact    `json:"development"`
	Audit       graph.Artifact    `json:"audit"`
	ExistingFit graph.Artifact    `json:"existing_fit"`
}
type PackageInput struct {
	Build     BuildInput
	SessionID string
}
type EvaluationInput struct {
	Build                   BuildInput
	Plan                    Plan
	Fit, Development, Audit execution.RunResult
	Regression              *RegressionReport
}
type BuildFailure struct{ ID, Message string }
type BatchPlan struct {
	Graph execution.Graph
	Fit   graph.Artifact
	Batch Batch
}
type BatchFinish struct {
	ID     string
	Result execution.RunResult
	Error  string
}

func AuthorSessionID(buildID string, attempt int) string {
	return fmt.Sprintf("internal/build/%s/author/%d", buildID, attempt)
}

func graphChild(ctx workflow.Context, id string, g execution.Graph, input, fit graph.Artifact) (execution.RunResult, error) {
	var result execution.RunResult
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: id, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE})
	err := workflow.ExecuteChildWorkflow(ctx, execution.GraphWorkflow, execution.RunInput{ExecutionID: id, Graph: g, Input: input, Fit: fit}).Get(ctx, &result)
	return result, err
}

// BuildWorkflow has a fixed interpreter: only the retained node manifests and
// sources evolve. No generated program can change this orchestration or approve.
func BuildWorkflow(ctx workflow.Context, in BuildInput) (releaseID string, err error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	defer func() {
		if err != nil {
			cleanup, _ := workflow.NewDisconnectedContext(ctx)
			_ = workflow.ExecuteActivity(cleanup, "PipelineBuildFailed", BuildFailure{in.ID, err.Error()}).Get(cleanup, nil)
		}
	}()
	var prompt string
	if err = workflow.ExecuteActivity(ctx, "PipelinePrepare", in).Get(ctx, &prompt); err != nil {
		return "", err
	}
	var plan Plan
	previous := ""
	attempts := 2
	if in.AutoReviewID != "" {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		session := AuthorSessionID(in.ID, attempt)
		authorCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: session, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE})
		var outcome agentruntime.Outcome
		input := agentruntime.Input{ID: session, Task: in.Task, Prompt: prompt, MaxTurns: in.MaxTurns, DatasetID: in.DatasetID, ParentReleaseID: in.ParentReleaseID, TargetNode: in.NodeID, ResumeSessionID: previous}
		if err = workflow.ExecuteChildWorkflow(authorCtx, agentruntime.AgentWorkflow, input).Get(ctx, &outcome); err != nil {
			return "", err
		}
		err = workflow.ExecuteActivity(ctx, "PipelinePackage", PackageInput{in, session}).Get(ctx, &plan)
		if err == nil {
			break
		}
		if attempt == attempts-1 {
			return "", err
		}
		message := err.Error()
		if len(message) > 3000 {
			message = message[:3000]
		}
		prompt = "Repair the retained candidate and source files. The trusted packager rejected them: " + message + "\nOriginal task: " + in.Prompt
		previous = session
	}
	fit := execution.RunResult{Output: plan.ExistingFit}
	if plan.ExistingFit.Digest == "" {
		if err = workflow.ExecuteActivity(ctx, "PipelinePhase", BuildFailure{in.ID, "training"}).Get(ctx, nil); err != nil {
			return "", err
		}
		if fit, err = graphChild(ctx, "internal/build/"+in.ID+"/fit", plan.Graph.Fit, plan.Train, graph.Artifact{}); err != nil {
			return "", err
		}
	}
	if err = workflow.ExecuteActivity(ctx, "PipelinePhase", BuildFailure{in.ID, "evaluating_development"}).Get(ctx, nil); err != nil {
		return "", err
	}
	dev, e := graphChild(ctx, "internal/build/"+in.ID+"/development", plan.Graph.Serve, plan.Development, fit.Output)
	if e != nil {
		return "", e
	}
	if err = workflow.ExecuteActivity(ctx, "PipelinePhase", BuildFailure{in.ID, "evaluating_audit"}).Get(ctx, nil); err != nil {
		return "", err
	}
	audit, e := graphChild(ctx, "internal/build/"+in.ID+"/audit", plan.Graph.Serve, plan.Audit, fit.Output)
	if e != nil {
		return "", e
	}
	var regression *RegressionReport
	if in.Regression != nil {
		if err = workflow.ExecuteActivity(ctx, "PipelinePhase", BuildFailure{in.ID, "replaying_trigger"}).Get(ctx, nil); err != nil {
			return "", err
		}
		replay, replayErr := graphChild(ctx, "internal/build/"+in.ID+"/regression", plan.Graph.Serve, in.Regression.Input, fit.Output)
		if replayErr != nil {
			return "", replayErr
		}
		var report RegressionReport
		if err = workflow.ExecuteActivity(ctx, "PipelineValidateRegression", RegressionValidation{Build: in, Result: replay}).Get(ctx, &report); err != nil {
			return "", err
		}
		regression = &report
	}
	err = workflow.ExecuteActivity(ctx, "PipelineEvaluate", EvaluationInput{Build: in, Plan: plan, Fit: fit, Development: dev, Audit: audit, Regression: regression}).Get(ctx, &releaseID)
	return releaseID, err
}

// BatchWorkflow can only resolve an already-human-approved immutable release.
// Neither it nor GraphWorkflow has a model client or a Hindsight dependency.
func BatchWorkflow(ctx workflow.Context, in BatchInput) (out execution.RunResult, err error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	defer func() {
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		f := BatchFinish{ID: in.ID, Result: out}
		if err != nil {
			f.Error = err.Error()
		}
		if e := workflow.ExecuteActivity(cleanup, "PipelineBatchFinish", f).Get(cleanup, nil); e != nil && err == nil {
			err = e
		}
	}()
	var plan BatchPlan
	if err = workflow.ExecuteActivity(ctx, "PipelineBatchPrepare", in).Get(ctx, &plan); err != nil {
		return out, err
	}
	return graphChild(ctx, "internal/batch/"+in.ID+"/serve", plan.Graph, in.Input, plan.Fit)
}

type Activities struct{ Service *Service }

func (a *Activities) PipelinePrepare(ctx context.Context, in BuildInput) (string, error) {
	s := a.Service
	if in.AutoReviewID != "" {
		review, err := s.Review(ctx, in.AutoReviewID)
		if err != nil {
			return "", err
		}
		if graph.Digest(review.Build) != graph.Digest(in) || in.Regression == nil {
			return "", fmt.Errorf("automatic review input changed")
		}
	}
	if in.Regression != nil {
		if _, err := s.regressionBatch(ctx, in); err != nil {
			return "", err
		}
	}
	d, err := s.Datasets.Get(ctx, in.DatasetID)
	if err != nil {
		return "", err
	}
	if d.Task != in.Task {
		return "", fmt.Errorf("dataset/task mismatch")
	}
	if in.ParentReleaseID != "" {
		p, err := s.Release(ctx, in.ParentReleaseID)
		if err != nil {
			return "", err
		}
		if p.DatasetID != d.ID || p.Task != in.Task {
			return "", fmt.Errorf("node tuning must use incumbent dataset and task")
		}
		if in.NodeID == "" {
			return "", fmt.Errorf("select one fit:node or serve:node for tuning")
		}
	}
	if err = s.UpdateJob(ctx, in.ID, "running", "authoring", "", AuthorSessionID(in.ID, 0), ""); err != nil {
		return "", err
	}
	policy, _ := json.Marshal(d.Policy)
	prompt := fmt.Sprintf("Build and test an executable v2 graph candidate from the supplied CSV training data. Task objective: %s\nDataset: %s; target=%s; positive=%s; declared label meaning=%s. Frozen evaluation policy=%s. Read graph-authoring.md for the required Python function interface and profile.json for feature types and split counts. Write candidate.json and self-contained per-node source modules. Do not stop at a graph proposal. No initial domain nodes are supplied.", in.Prompt, d.ID, d.Target, d.PositiveLabel, d.LabelMeaning, policy)
	if in.ParentReleaseID != "" {
		prompt = fmt.Sprintf("Improve ONLY node %s in the supplied incumbent candidate.json. Change its source and/or config; every other node and the topology must remain identical. Preserve compatible fitted state. Task request: %s\nFrozen policy=%s. Compare development evidence and tests. Do not approve/promote anything.", in.NodeID, in.Prompt, policy)
	}
	return prompt, nil
}
func (a *Activities) PipelinePhase(ctx context.Context, in BuildFailure) error {
	return a.Service.UpdateJob(ctx, in.ID, "running", in.Message, "", "", "")
}
func (a *Activities) PipelinePackage(ctx context.Context, in PackageInput) (Plan, error) {
	s := a.Service
	var plan Plan
	if err := s.UpdateJob(ctx, in.Build.ID, "running", "packaging", "", in.SessionID, ""); err != nil {
		return plan, err
	}
	root, err := os.OpenRoot(s.Workspace(in.SessionID))
	if err != nil {
		return plan, err
	}
	defer root.Close()
	file, err := root.OpenFile("candidate.json", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return plan, fmt.Errorf("candidate.json is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return plan, fmt.Errorf("candidate.json must be a regular v2 manifest <=64KiB")
	}
	b, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return plan, err
	}
	var draft execution.Draft
	if len(b) > 64<<10 {
		return plan, fmt.Errorf("candidate.json exceeds64KiB")
	}
	if err = graph.Decode(b, &draft); err != nil {
		return plan, fmt.Errorf("candidate.json: %w", err)
	}
	engine := *s.Engine
	if in.Build.ParentReleaseID != "" {
		p, e := s.Release(ctx, in.Build.ParentReleaseID)
		if e != nil {
			return plan, e
		}
		engine.Image = p.Graph.Fit.Nodes[0].Image
	}
	if plan.Graph, err = engine.Package(ctx, s.Workspace(in.SessionID), draft); err != nil {
		return plan, err
	}
	if in.Build.ParentReleaseID != "" {
		parent, err := s.Release(ctx, in.Build.ParentReleaseID)
		if err != nil {
			return plan, err
		}
		if err = execution.ValidateTuning(parent.Graph, plan.Graph, in.Build.NodeID); err != nil {
			return plan, err
		}
		if in.Build.AutoReviewID != "" && parent.Graph.ID == plan.Graph.ID {
			return plan, fmt.Errorf("automatic revision did not change the selected node")
		}
		if strings.HasPrefix(in.Build.NodeID, "serve:") {
			plan.ExistingFit = parent.FitArtifact
		}
	}
	if plan.Train, err = s.Datasets.Inputs(ctx, in.Build.DatasetID, "train", true); err != nil {
		return plan, err
	}
	if plan.Development, err = s.Datasets.Inputs(ctx, in.Build.DatasetID, "development", false); err != nil {
		return plan, err
	}
	plan.Audit, err = s.Datasets.Inputs(ctx, in.Build.DatasetID, "audit", false)
	if err != nil {
		return plan, err
	}
	provisional := Release{ID: "draft-" + strings.TrimPrefix(graph.Hash([]byte(in.Build.ID+"/"+plan.Graph.ID)), "sha256:"), Task: in.Build.Task, DatasetID: in.Build.DatasetID, ParentReleaseID: in.Build.ParentReleaseID, NodeID: in.Build.NodeID, Status: "evaluating", Graph: plan.Graph, FitArtifact: plan.ExistingFit, CreatedAt: time.Now().UTC()}
	if err = s.SaveRelease(ctx, provisional); err != nil {
		return plan, err
	}
	if err = s.UpdateJob(ctx, in.Build.ID, "running", "packaged", "", in.SessionID, provisional.ID); err != nil {
		return plan, err
	}
	return plan, err
}
func (a *Activities) PipelineEvaluate(ctx context.Context, in EvaluationInput) (string, error) {
	s := a.Service
	if in.Build.Regression != nil && (in.Regression == nil || !in.Regression.Passed || in.Regression.BatchID != in.Build.Regression.BatchID) {
		return "", fmt.Errorf("candidate requires a successful triggering-batch replay")
	}
	dev, err := s.Datasets.Evaluate(ctx, in.Build.DatasetID, "development", in.Development.Output)
	if err != nil {
		return "", err
	}
	audit, err := s.Datasets.Evaluate(ctx, in.Build.DatasetID, "audit", in.Audit.Output)
	if err != nil {
		return "", err
	}
	if in.Build.ParentReleaseID != "" {
		parent, err := s.Release(ctx, in.Build.ParentReleaseID)
		if err != nil {
			return "", err
		}
		if dev.Metrics.F1+1e-9 < parent.Development.Metrics.F1 {
			dev.Passed = false
			dev.Failures = append(dev.Failures, "development F1 regressed against the immutable incumbent")
		}
	}
	identity := map[string]any{"graph": in.Plan.Graph, "fit_artifact": in.Fit.Output, "dataset_id": in.Build.DatasetID}
	if in.Build.AutoReviewID != "" {
		identity["auto_review_id"] = in.Build.AutoReviewID
	}
	id := graph.Digest(identity)
	status := "rejected"
	if dev.Passed && audit.Passed {
		status = "eligible"
	}
	r := Release{ID: id, Task: in.Build.Task, DatasetID: in.Build.DatasetID, ParentReleaseID: in.Build.ParentReleaseID, NodeID: in.Build.NodeID, Status: status, Graph: in.Plan.Graph, FitArtifact: in.Fit.Output, Development: dev, Audit: audit, FitTrace: in.Fit.Nodes, DevelopmentTrace: in.Development.Nodes, AuditTrace: in.Audit.Nodes, CreatedAt: time.Now().UTC()}
	r.AutoReviewID, r.Regression = in.Build.AutoReviewID, in.Regression
	if err = s.SaveRelease(ctx, r); err != nil {
		return "", err
	}
	if j, e := s.Job(ctx, in.Build.ID); e == nil && strings.HasPrefix(j.ReleaseID, "draft-") {
		if pending, e := s.Release(ctx, j.ReleaseID); e == nil {
			pending.Status = "superseded"
			if e = s.UpdateCandidate(ctx, pending); e != nil {
				return "", e
			}
		}
	}
	if err = s.UpdateJob(ctx, in.Build.ID, "completed", "awaiting_human_approval", "", "", id); err != nil {
		return "", err
	}
	if status == "rejected" {
		if err = s.UpdateJob(ctx, in.Build.ID, "completed", "evaluation_rejected", "", "", id); err != nil {
			return "", err
		}
	}
	// Development evidence may inform tuning; held-out audit results/labels are
	// not copied into the agent's memory or authoring workspace.
	evidence := fmt.Sprintf("Measured development evaluation for release %s, dataset %s: precision=%.4f recall=%.4f F1=%.4f FPR=%.4f, passed=%t. This measures the declared target on this frozen split, not universal fraud accuracy. Selected node=%s. No promotion occurred; explicit human approval is required.", id, in.Build.DatasetID, dev.Metrics.Precision, dev.Metrics.Recall, dev.Metrics.F1, dev.Metrics.FalsePositiveRate, dev.Passed, in.Build.NodeID)
	if err = s.DB.Remember(ctx, in.Build.Task, "execution_evaluation", evidence, in.Build.ID+"/evaluation"); err != nil {
		return "", err
	}
	// Delivery is retried by the next authoring Setup if currently unavailable.
	_ = s.DB.SyncMemory(ctx, s.Hindsight, in.Build.Task)
	return id, nil
}
func (a *Activities) PipelineBuildFailed(ctx context.Context, in BuildFailure) error {
	message := in.Message
	if len(message) > 4000 {
		message = message[:4000]
	}
	s := a.Service
	if err := s.UpdateJob(ctx, in.ID, "failed", "failed", message, "", ""); err != nil {
		return err
	}
	j, err := s.Job(ctx, in.ID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(j.ReleaseID, "draft-") {
		if r, e := s.Release(ctx, j.ReleaseID); e == nil {
			r.Status = "execution_failed"
			r.ExecutionError = message
			if e = s.UpdateCandidate(ctx, r); e != nil {
				return e
			}
		}
	}
	if j.AutoReviewID != "" {
		message = "Automatic robustness candidate failed authoring, packaging, evaluation, or replay. Inspect the retained job for operator diagnostics."
	}
	return s.DB.Remember(ctx, j.Task, "execution_error", "Graph build failed: "+message, in.ID+"/build_failure")
}
func (a *Activities) PipelineBatchPrepare(ctx context.Context, in BatchInput) (BatchPlan, error) {
	s := a.Service
	var p BatchPlan
	b, err := s.Batch(ctx, in.ID)
	if err != nil {
		return p, err
	}
	if graph.Digest(b.BatchInput) != graph.Digest(in) {
		return p, fmt.Errorf("batch input identity changed")
	}
	r, err := s.Release(ctx, in.ReleaseID)
	if err != nil {
		return p, err
	}
	if r.ApprovedAt == nil {
		return p, fmt.Errorf("release has not been approved by a human")
	}
	// A resubmission after Temporal retention expires can re-enter Prepare.
	// Preserve the terminal batch and its once-only completion signal.
	if b.Status != "completed" && b.Status != "failed" {
		b.Status = "running"
		if err = s.SaveBatch(ctx, b); err != nil {
			return p, err
		}
	}
	p = BatchPlan{Graph: r.Graph.Serve, Fit: r.FitArtifact, Batch: b}
	return p, nil
}
func (a *Activities) PipelineBatchFinish(ctx context.Context, in BatchFinish) error {
	s := a.Service
	b, err := s.Batch(ctx, in.ID)
	if err != nil {
		return err
	}
	b.Status = "completed"
	b.Error = in.Error
	b.Trace = in.Result.Nodes
	b.Output = in.Result.Output
	if in.Error == "" {
		if e := s.Datasets.ValidateOutput(ctx, b.Input, in.Result.Output); e != nil {
			b.Error = "invalid batch predictions: " + e.Error()
			b.Status = "failed"
			b.Output = graph.Artifact{}
		}
	}
	if in.Error != "" {
		b.Status = "failed"
	}
	return s.recordBatchSignal(ctx, b)
}

type RegressionValidation struct {
	Build  BuildInput
	Result execution.RunResult
}

func (s *Service) regressionBatch(ctx context.Context, in BuildInput) (Batch, error) {
	if in.Regression == nil {
		return Batch{}, fmt.Errorf("regression input is required")
	}
	b, err := s.Batch(ctx, in.Regression.BatchID)
	if err != nil {
		return b, err
	}
	if b.Task != in.Task || b.ReleaseID != in.ParentReleaseID || b.Status != "failed" || b.Input != in.Regression.Input {
		return b, fmt.Errorf("regression must replay a failed batch of the incumbent release")
	}
	return b, nil
}

func (a *Activities) PipelineValidateRegression(ctx context.Context, in RegressionValidation) (RegressionReport, error) {
	s := a.Service
	b, err := s.regressionBatch(ctx, in.Build)
	if err != nil {
		return RegressionReport{}, err
	}
	if err = s.Datasets.ValidateOutput(ctx, b.Input, in.Result.Output); err != nil {
		return RegressionReport{}, err
	}
	return RegressionReport{BatchID: b.ID, Passed: true, Rows: b.RowCount, Before: b.Status, After: "completed", Trace: in.Result.Nodes}, nil
}
