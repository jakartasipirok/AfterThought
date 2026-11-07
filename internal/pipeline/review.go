package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const ReviewBatchInterval = 50
const ReviewTurnBudget = 12
const ReviewCooldown = 10 * time.Minute

type RegressionSpec struct {
	BatchID string         `json:"batch_id"`
	Input   graph.Artifact `json:"input"`
}
type RegressionReport struct {
	BatchID string                 `json:"batch_id"`
	Passed  bool                   `json:"passed"`
	Rows    int                    `json:"rows"`
	Before  string                 `json:"before"`
	After   string                 `json:"after"`
	Trace   []execution.NodeResult `json:"trace"`
}
type BatchSignal struct {
	Sequence  int64          `json:"sequence"`
	BatchID   string         `json:"batch_id"`
	Kind      string         `json:"kind"`
	NodeID    string         `json:"node_id,omitempty"`
	Rows      int            `json:"rows"`
	Missing   map[string]int `json:"missing"`
	CreatedAt time.Time      `json:"created_at"`
}
type AutoReview struct {
	ID           string         `json:"id"`
	Task         string         `json:"task"`
	ParentID     string         `json:"parent_release_id"`
	Status       string         `json:"status"`
	Build        BuildInput     `json:"build"`
	CandidateID  string         `json:"candidate_id,omitempty"`
	Summary      string         `json:"summary"`
	FailureCount int            `json:"failure_count"`
	Rows         int            `json:"rows"`
	Missing      map[string]int `json:"missing"`
	CreatedAt    time.Time      `json:"created_at"`
}
type AutoReviewStatus struct {
	Enabled         bool        `json:"enabled"`
	ReleaseID       string      `json:"active_release_id"`
	Interval        int         `json:"interval"`
	Finished        int         `json:"finished_batches"`
	Remaining       int         `json:"remaining_batches"`
	EstimateSeconds *int64      `json:"estimate_seconds,omitempty"`
	CooldownUntil   time.Time   `json:"cooldown_until"`
	MaxTurns        int         `json:"max_turns"`
	Latest          *AutoReview `json:"latest,omitempty"`
}

func (s *Service) migrateAutoReview(ctx context.Context) error {
	_, err := s.DB.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS graph_review_settings(task_id text PRIMARY KEY, enabled boolean NOT NULL DEFAULT false, after_seq bigint NOT NULL DEFAULT 0, enabled_at timestamptz NOT NULL DEFAULT now(), next_allowed_at timestamptz NOT NULL DEFAULT '1970-01-01');
CREATE TABLE IF NOT EXISTS graph_batch_signals(seq bigserial PRIMARY KEY, batch_id text NOT NULL UNIQUE REFERENCES graph_batches(id), task_id text NOT NULL, release_id text NOT NULL, payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS graph_batch_signals_window ON graph_batch_signals(task_id,release_id,seq);
CREATE TABLE IF NOT EXISTS graph_auto_reviews(id text PRIMARY KEY, task_id text NOT NULL, parent_id text NOT NULL, status text NOT NULL, candidate_id text NOT NULL DEFAULT '', payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS graph_auto_reviews_pending ON graph_auto_reviews(task_id,parent_id,status);`)
	return err
}

// Signals contain counts and trusted node identifiers, never source rows, model
// stderr, predictions, or human-supplied diagnostic text. Retries count once.
func (s *Service) recordBatchSignal(ctx context.Context, b Batch) error {
	input, err := s.Files.Get(b.Input)
	if err != nil {
		return err
	}
	var data dataset.Data
	if err = json.Unmarshal(input, &data); err != nil {
		return err
	}
	signal := BatchSignal{BatchID: b.ID, Kind: "healthy", Rows: len(data.Rows), Missing: map[string]int{}, CreatedAt: time.Now().UTC()}
	for _, row := range data.Rows {
		for field, value := range row {
			if value == nil {
				signal.Missing[field]++
			}
		}
	}
	if b.Status == "failed" {
		signal.Kind = "unlocalized_failure"
		trace := b.Trace
		if len(trace) == 0 && s.Temporal != nil {
			value, e := s.Temporal.QueryWorkflow(ctx, "internal/batch/"+b.ID+"/serve", "", "progress")
			if e == nil {
				var progress execution.RunResult
				if value.Get(&progress) == nil {
					trace = progress.Nodes
					b.Trace = trace
				}
			}
		}
		for _, node := range trace {
			if node.Status == "failed" {
				signal.NodeID = "serve:" + node.ID
				signal.Kind = "node_failure"
				break
			}
		}
		if strings.HasPrefix(b.Error, "invalid batch predictions:") {
			release, e := s.Release(ctx, b.ReleaseID)
			if e != nil {
				return e
			}
			signal.NodeID = "serve:" + release.Graph.Serve.Output
			signal.Kind = "invalid_output"
		}
	}
	// Serialize sequence allocation and commit with window consumption. A late
	// commit must never appear behind an already-consumed sequence cursor.
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, b.Task); err != nil {
		return err
	}
	var recorded bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM graph_batch_signals WHERE batch_id=$1)`, b.ID).Scan(&recorded); err != nil {
		return err
	}
	if recorded {
		return nil
	}
	b.UpdatedAt = signal.CreatedAt
	if _, err = tx.Exec(ctx, `UPDATE graph_batches SET payload=$2 WHERE id=$1`, b.ID, encode(b)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO graph_batch_signals(batch_id,task_id,release_id,payload) VALUES($1,$2,$3,$4)`, b.ID, b.Task, b.ReleaseID, encode(signal)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) SetAutoReview(ctx context.Context, task string, enabled bool) error {
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, task); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO graph_review_settings(task_id,enabled,after_seq) VALUES($1,$2,(SELECT coalesce(max(seq),0) FROM graph_batch_signals WHERE task_id=$1)) ON CONFLICT(task_id) DO UPDATE SET enabled=$2, after_seq=CASE WHEN $2 AND NOT graph_review_settings.enabled THEN (SELECT coalesce(max(seq),0) FROM graph_batch_signals WHERE task_id=$1) ELSE graph_review_settings.after_seq END, enabled_at=CASE WHEN $2 AND NOT graph_review_settings.enabled THEN now() ELSE graph_review_settings.enabled_at END`, task, enabled)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Review(ctx context.Context, id string) (AutoReview, error) {
	var r AutoReview
	var b []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM graph_auto_reviews WHERE id=$1`, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}

func (s *Service) AutoReviewStatus(ctx context.Context, task string) (AutoReviewStatus, error) {
	out := AutoReviewStatus{Interval: ReviewBatchInterval, Remaining: ReviewBatchInterval, MaxTurns: ReviewTurnBudget}
	var after int64
	err := s.DB.Pool.QueryRow(ctx, `SELECT enabled,after_seq,next_allowed_at FROM graph_review_settings WHERE task_id=$1`, task).Scan(&out.Enabled, &after, &out.CooldownUntil)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if out.ReleaseID, err = s.Active(ctx, task); err != nil {
		return out, err
	}
	if out.Enabled && out.ReleaseID != "" {
		var first, last *time.Time
		err = s.DB.Pool.QueryRow(ctx, `SELECT count(*),min(created_at),max(created_at) FROM graph_batch_signals WHERE task_id=$1 AND release_id=$2 AND seq>$3`, task, out.ReleaseID, after).Scan(&out.Finished, &first, &last)
		if err != nil {
			return out, err
		}
		out.Remaining = max(0, ReviewBatchInterval-out.Finished)
		if out.Finished >= 2 && last != nil && first != nil && last.After(*first) {
			seconds := int64(last.Sub(*first).Seconds() * float64(out.Remaining) / float64(out.Finished-1))
			out.EstimateSeconds = &seconds
		}
	}
	var b []byte
	err = s.DB.Pool.QueryRow(ctx, `SELECT payload FROM graph_auto_reviews WHERE task_id=$1 AND parent_id=$2 ORDER BY created_at DESC LIMIT 1`, task, out.ReleaseID).Scan(&b)
	if err == nil {
		var r AutoReview
		if err = json.Unmarshal(b, &r); err != nil {
			return out, err
		}
		out.Latest = &r
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, nil
}

// ClaimReview consumes exactly one 50-batch window while holding the task lock.
// It reserves durable work before the Temporal dispatch; lost start acks are safe.
func (s *Service) ClaimReview(ctx context.Context, task string) (*AutoReview, error) {
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, task); err != nil {
		return nil, err
	}
	var enabled bool
	var after int64
	var cooldown time.Time
	err = tx.QueryRow(ctx, `SELECT enabled,after_seq,next_allowed_at FROM graph_review_settings WHERE task_id=$1 FOR UPDATE`, task).Scan(&enabled, &after, &cooldown)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !enabled || time.Now().Before(cooldown) {
		return nil, nil
	}
	var parent Release
	var b []byte
	err = tx.QueryRow(ctx, `SELECT r.payload FROM graph_channels c JOIN graph_releases r ON r.id=c.release_id WHERE c.task_id=$1`, task).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &parent); err != nil {
		return nil, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM graph_auto_reviews WHERE task_id=$1 AND parent_id=$2 AND status IN ('queued','running','awaiting_approval'))`, task, parent.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT seq,payload FROM graph_batch_signals WHERE task_id=$1 AND release_id=$2 AND seq>$3 ORDER BY seq LIMIT $4`, task, parent.ID, after, ReviewBatchInterval)
	if err != nil {
		return nil, err
	}
	var signals []BatchSignal
	for rows.Next() {
		var signal BatchSignal
		var raw []byte
		var seq int64
		if err = rows.Scan(&seq, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &signal); err != nil {
			break
		}
		signal.Sequence = seq
		signals = append(signals, signal)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(signals) < ReviewBatchInterval {
		return nil, nil
	}
	r := AutoReview{Task: task, ParentID: parent.ID, Status: "no_action", CreatedAt: time.Now().UTC(), Missing: map[string]int{}}
	var trigger *BatchSignal
	for i := range signals {
		signal := &signals[i]
		r.Rows += signal.Rows
		for name, count := range signal.Missing {
			r.Missing[name] += count
		}
		if signal.Kind != "healthy" {
			r.FailureCount++
		}
		if signal.NodeID != "" && (signal.Kind == "node_failure" || signal.Kind == "invalid_output") {
			for _, node := range parent.Graph.Serve.Nodes {
				if "serve:"+node.ID == signal.NodeID {
					trigger = signal
				}
			}
		}
	}
	last := signals[len(signals)-1].Sequence
	r.ID = "auto-review-" + strings.TrimPrefix(graph.Hash([]byte(fmt.Sprintf("%s/%s/%d", task, parent.ID, last))), "sha256:")[:40]
	r.Summary = fmt.Sprintf("Checked %d finished batches with %d failures. Across %d rows, missing-value counts are %s. This window required no authoring-model calls.", ReviewBatchInterval, r.FailureCount, r.Rows, encode(r.Missing))
	if trigger != nil {
		var batch Batch
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT payload FROM graph_batches WHERE id=$1`, trigger.BatchID).Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		r.Status = "queued"
		r.Summary = fmt.Sprintf("Review after %d finished batches with %d failures. The trigger is %s at %s. Across %d rows, missing-value counts are %s. Replay the triggering batch and evaluate the repair before human review.", ReviewBatchInterval, r.FailureCount, trigger.Kind, trigger.NodeID, r.Rows, encode(r.Missing))
		r.Build = BuildInput{ID: r.ID, Task: task, DatasetID: parent.DatasetID, ParentReleaseID: parent.ID, NodeID: trigger.NodeID, MaxTurns: ReviewTurnBudget, AutoReviewID: r.ID, Regression: &RegressionSpec{BatchID: batch.ID, Input: batch.Input}, Prompt: r.Summary + " Improve only the selected node's robustness. Preserve probabilities and decisions on valid inputs. Add focused synthetic tests for the failure, compatible feature types and allowed nulls. Use existing development evidence and attributed memory. Never approve or activate."}
		cooldown = r.CreatedAt.Add(ReviewCooldown)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO graph_auto_reviews(id,task_id,parent_id,status,payload) VALUES($1,$2,$3,$4,$5)`, r.ID, task, parent.ID, r.Status, encode(r)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE graph_review_settings SET after_seq=$2,next_allowed_at=$3 WHERE task_id=$1`, task, last, cooldown); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_memory(task_id,source,content,dedup_key) VALUES($1,'automatic_review',$2,$3) ON CONFLICT DO NOTHING`, task, r.Summary, r.ID+"/trigger"); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) DispatchAutoReviews(ctx context.Context, queue string) error {
	if s.Temporal == nil {
		return fmt.Errorf("automatic review requires a Temporal client")
	}
	if err := s.reconcileAutoReviews(ctx); err != nil {
		return err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT task_id FROM graph_review_settings WHERE enabled ORDER BY task_id`)
	if err != nil {
		return err
	}
	var tasks []string
	for rows.Next() {
		var task string
		if err = rows.Scan(&task); err != nil {
			break
		}
		tasks = append(tasks, task)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, task := range tasks {
		if _, err = s.ClaimReview(ctx, task); err != nil {
			return err
		}
	}
	rows, err = s.DB.Pool.Query(ctx, `SELECT id FROM graph_auto_reviews WHERE status='queued' ORDER BY created_at LIMIT 20`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		_, err = s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "internal/review/" + id, TaskQueue: queue, WorkflowExecutionTimeout: 30 * time.Minute, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, AutoReviewWorkflow, id)
		var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &duplicate) {
			return err
		}
	}
	return nil
}

type ReviewWork struct{ Build *BuildInput }
type ReviewFinish struct {
	ID, CandidateID string
	Failed          bool
}

// Separate learning workflow: serving histories and their zero-model counters
// remain unchanged. Only the opted-in review workflow can invoke authoring.
func AutoReviewWorkflow(ctx workflow.Context, id string) (candidate string, err error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	defer func() {
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		e := workflow.ExecuteActivity(cleanup, "PipelineAutoReviewFinish", ReviewFinish{ID: id, CandidateID: candidate, Failed: err != nil}).Get(cleanup, nil)
		if err == nil {
			err = e
		}
	}()
	var work ReviewWork
	if err = workflow.ExecuteActivity(ctx, "PipelineAutoReviewPrepare", id).Get(ctx, &work); err != nil {
		return "", err
	}
	if work.Build == nil {
		return "", nil
	}
	child := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: work.Build.ID, WorkflowExecutionTimeout: 20 * time.Minute, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE})
	err = workflow.ExecuteChildWorkflow(child, BuildWorkflow, *work.Build).Get(ctx, &candidate)
	return candidate, err
}

func (a *Activities) PipelineAutoReviewPrepare(ctx context.Context, id string) (ReviewWork, error) {
	s := a.Service
	r, err := s.Review(ctx, id)
	if err != nil {
		return ReviewWork{}, err
	}
	status, err := s.AutoReviewStatus(ctx, r.Task)
	if err != nil {
		return ReviewWork{}, err
	}
	if !status.Enabled || status.ReleaseID != r.ParentID {
		r.Status = "cancelled"
		_, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status=$2,payload=$3 WHERE id=$1`, id, r.Status, encode(r))
		return ReviewWork{}, err
	}
	if r.Status != "queued" && r.Status != "running" {
		return ReviewWork{}, nil
	}
	if _, _, err = s.AdmitBuild(ctx, r.Build); err != nil {
		return ReviewWork{}, err
	}
	r.Status = "running"
	_, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status=$2,payload=$3 WHERE id=$1`, id, r.Status, encode(r))
	return ReviewWork{Build: &r.Build}, err
}
func (a *Activities) PipelineAutoReviewFinish(ctx context.Context, in ReviewFinish) error {
	s := a.Service
	r, err := s.Review(ctx, in.ID)
	if err != nil {
		return err
	}
	if r.Status == "cancelled" {
		return nil
	}
	if r.Status == "approved" {
		in.CandidateID, in.Failed = r.CandidateID, false
	}
	r.Status = "failed"
	r.CandidateID = in.CandidateID
	measurement := ""
	if !in.Failed && in.CandidateID != "" {
		candidate, e := s.Release(ctx, in.CandidateID)
		if e != nil {
			return e
		}
		if candidate.AutoReviewID != r.ID || candidate.ParentReleaseID != r.ParentID {
			return fmt.Errorf("review candidate identity mismatch")
		}
		if candidate.Regression != nil {
			measurement = fmt.Sprintf(" Triggering-batch replay passed=%t for %d rows. Measured development F1=%.4f.", candidate.Regression.Passed, candidate.Regression.Rows, candidate.Development.Metrics.F1)
		}
		if candidate.Status == "eligible" && candidate.Regression != nil && candidate.Regression.Passed {
			r.Status = "awaiting_approval"
			if candidate.ApprovedAt != nil {
				r.Status = "approved"
			}
		} else {
			r.Status = "rejected"
		}
	}
	_, err = s.DB.Pool.Exec(ctx, `UPDATE graph_auto_reviews SET status=$2,candidate_id=$3,payload=$4 WHERE id=$1 AND status IN ('queued','running')`, r.ID, r.Status, r.CandidateID, encode(r))
	if err != nil {
		return err
	}
	evidence := fmt.Sprintf("Automatic robustness review %s targeted node %s. The result is %s with candidate %s. Frozen development gates and a triggering-batch regression replay are required. Activation requires human approval.", r.ID, r.Build.NodeID, r.Status, r.CandidateID) + measurement
	if err = s.DB.Remember(ctx, r.Task, "automatic_review", evidence, r.ID+"/outcome"); err != nil {
		return err
	}
	_ = s.DB.SyncMemory(ctx, s.Hindsight, r.Task)
	return nil
}

// A worker or workflow timeout can prevent its cleanup Activity from running.
// Reconcile terminal executions before admitting another review window.
func (s *Service) reconcileAutoReviews(ctx context.Context) error {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id FROM graph_auto_reviews WHERE status IN ('queued','running') ORDER BY created_at LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		description, err := s.Temporal.DescribeWorkflowExecution(ctx, "internal/review/"+id, "")
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			review, lookupErr := s.Review(ctx, id)
			if lookupErr != nil {
				return lookupErr
			}
			if review.Status == "queued" {
				continue
			}
			// A previously running execution can age out of Temporal retention.
			// Its durable build journal still determines the review outcome.
		} else {
			if err != nil {
				return err
			}
			if description.GetWorkflowExecutionInfo().GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				continue
			}
		}
		finish := ReviewFinish{ID: id, Failed: true}
		job, err := s.Job(ctx, id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && job.Status == "completed" && job.ReleaseID != "" {
			finish.CandidateID, finish.Failed = job.ReleaseID, false
		}
		if err = (&Activities{Service: s}).PipelineAutoReviewFinish(ctx, finish); err != nil {
			return err
		}
	}
	return nil
}
