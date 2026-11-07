// Package pipeline joins dataset discovery, graph authoring, evaluation, and
// human-approved release selection. Graph execution itself has no model client.
package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/client"
)

var ErrConflict = errors.New("identifier already belongs to a different request")
var ErrApproval = errors.New("a valid host-only human approval key and reviewer are required")

type Service struct {
	DB        *storage.DB
	Files     *storage.Files
	Datasets  *dataset.Store
	Engine    *execution.Engine
	Hindsight *memory.Hindsight
	Temporal  client.Client
}

type BuildInput struct {
	ID              string          `json:"id"`
	Task            string          `json:"task"`
	DatasetID       string          `json:"dataset_id"`
	Prompt          string          `json:"prompt"`
	MaxTurns        int             `json:"max_turns"`
	ParentReleaseID string          `json:"parent_release_id,omitempty"`
	NodeID          string          `json:"node_id,omitempty"`
	AutoReviewID    string          `json:"auto_review_id,omitempty"`
	Regression      *RegressionSpec `json:"regression,omitempty"`
}
type Job struct {
	BuildInput
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Error     string    `json:"error,omitempty"`
	SessionID string    `json:"session_id"`
	ReleaseID string    `json:"release_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Release struct {
	ID               string                 `json:"id"`
	Task             string                 `json:"task"`
	DatasetID        string                 `json:"dataset_id"`
	ParentReleaseID  string                 `json:"parent_release_id,omitempty"`
	NodeID           string                 `json:"node_id,omitempty"`
	Status           string                 `json:"status"`
	ExecutionError   string                 `json:"execution_error,omitempty"`
	Graph            execution.Release      `json:"graph"`
	FitArtifact      graph.Artifact         `json:"fit_artifact"`
	Development      dataset.Report         `json:"development"`
	Audit            dataset.Report         `json:"audit"`
	FitTrace         []execution.NodeResult `json:"fit_trace"`
	DevelopmentTrace []execution.NodeResult `json:"development_trace"`
	AuditTrace       []execution.NodeResult `json:"audit_trace"`
	CreatedAt        time.Time              `json:"created_at"`
	ApprovedAt       *time.Time             `json:"approved_at,omitempty"`
	ApprovedBy       string                 `json:"approved_by,omitempty"`
	AutoReviewID     string                 `json:"auto_review_id,omitempty"`
	Regression       *RegressionReport      `json:"regression,omitempty"`
}
type BatchInput struct {
	ID        string         `json:"id"`
	ReleaseID string         `json:"release_id"`
	Input     graph.Artifact `json:"input"`
	RowCount  int            `json:"row_count"`
}
type Batch struct {
	BatchInput
	Task         string                 `json:"task"`
	Status       string                 `json:"status"`
	Error        string                 `json:"error,omitempty"`
	Output       graph.Artifact         `json:"output"`
	Trace        []execution.NodeResult `json:"trace"`
	ModelCalls   int                    `json:"model_calls"`
	InputTokens  int                    `json:"input_tokens"`
	OutputTokens int                    `json:"output_tokens"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func (s *Service) Migrate(ctx context.Context) error {
	if err := s.Datasets.Migrate(ctx); err != nil {
		return err
	}
	_, err := s.DB.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS graph_builds(id text PRIMARY KEY,task_id text NOT NULL,input_digest text NOT NULL,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS graph_releases(id text PRIMARY KEY,task_id text NOT NULL,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS graph_channels(task_id text PRIMARY KEY,release_id text NOT NULL REFERENCES graph_releases(id),updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS graph_approvals(id bigserial PRIMARY KEY,task_id text NOT NULL,release_id text NOT NULL REFERENCES graph_releases(id),previous_release_id text NOT NULL,actor text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS graph_batches(id text PRIMARY KEY,task_id text NOT NULL,input_digest text NOT NULL,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());`)
	if err != nil {
		return err
	}
	if err = s.migrateAutoReview(ctx); err != nil {
		return err
	}
	return s.ensureApprovalKey()
}
func ApprovalKeyPath(dataDir string) string { return filepath.Join(dataDir, "operator-approval.key") }

var approvalKeyFormat = regexp.MustCompile(`^[A-Z2-7]{26,128}$`)

func ReadApprovalKey(dataDir string) (string, error) {
	f, err := os.OpenFile(ApprovalKeyPath(dataDir), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 130 || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("host approval key must be a private regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 131))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if !approvalKeyFormat.MatchString(key) {
		return "", fmt.Errorf("host approval key is invalid or incomplete")
	}
	return key, nil
}
func (s *Service) ensureApprovalKey() error {
	path := ApprovalKeyPath(s.Files.Root)
	if _, err := ReadApprovalKey(s.Files.Root); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(s.Files.Root, ".approval-staging-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(rand.Text() + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	dir, err := os.Open(s.Files.Root)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return err
	}
	_, err = ReadApprovalKey(s.Files.Root)
	return err
}
func (s *Service) AdmitBuild(ctx context.Context, in BuildInput) (Job, bool, error) {
	now := time.Now().UTC()
	j := Job{BuildInput: in, Status: "queued", Phase: "queued", SessionID: AuthorSessionID(in.ID, 0), CreatedAt: now, UpdatedAt: now}
	tag, err := s.DB.Pool.Exec(ctx, `INSERT INTO graph_builds(id,task_id,input_digest,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, in.ID, in.Task, graph.Digest(in), encode(j))
	if err != nil {
		return j, false, err
	}
	if tag.RowsAffected() == 1 {
		return j, true, nil
	}
	var digest string
	var b []byte
	if err = s.DB.Pool.QueryRow(ctx, `SELECT input_digest,payload FROM graph_builds WHERE id=$1`, in.ID).Scan(&digest, &b); err != nil {
		return j, false, err
	}
	if digest != graph.Digest(in) {
		return j, false, ErrConflict
	}
	err = json.Unmarshal(b, &j)
	return j, false, err
}
func (s *Service) Job(ctx context.Context, id string) (Job, error) {
	var j Job
	var b []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM graph_builds WHERE id=$1`, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &j)
	}
	return j, err
}
func (s *Service) Jobs(ctx context.Context, task string) ([]Job, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT payload FROM graph_builds WHERE ($1='' OR task_id=$1) ORDER BY created_at DESC LIMIT 50`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var b []byte
		var j Job
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Service) UpdateJob(ctx context.Context, id, status, phase, message, session, release string) error {
	j, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	j.Status = status
	j.Phase = phase
	j.Error = message
	j.UpdatedAt = time.Now().UTC()
	if session != "" {
		j.SessionID = session
	}
	if release != "" {
		j.ReleaseID = release
	}
	_, err = s.DB.Pool.Exec(ctx, `UPDATE graph_builds SET payload=$2 WHERE id=$1`, id, encode(j))
	return err
}
func (s *Service) SaveRelease(ctx context.Context, r Release) error {
	_, err := s.DB.Pool.Exec(ctx, `INSERT INTO graph_releases(id,task_id,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.ID, r.Task, encode(r))
	return err
}
func (s *Service) UpdateCandidate(ctx context.Context, r Release) error {
	if !strings.HasPrefix(r.ID, "draft-") {
		return fmt.Errorf("finalized releases are immutable")
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE graph_releases SET payload=$2 WHERE id=$1`, r.ID, encode(r))
	return err
}
func (s *Service) Release(ctx context.Context, id string) (Release, error) {
	var r Release
	var b []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM graph_releases WHERE id=$1`, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}
func (s *Service) Releases(ctx context.Context, task string) ([]Release, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT payload FROM graph_releases WHERE ($1='' OR task_id=$1) AND payload->>'status'<>'superseded' ORDER BY created_at DESC LIMIT 50`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		var b []byte
		var r Release
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) Active(ctx context.Context, task string) (string, error) {
	var id string
	err := s.DB.Pool.QueryRow(ctx, `SELECT release_id FROM graph_channels WHERE task_id=$1`, task).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// Approve is deliberately absent from model tools and all Workflow/Activity
// registrations. The host-only key is never mounted in an agent or node sandbox.
func (s *Service) Approve(ctx context.Context, id, actor, key, expected string) error {
	secret, err := ReadApprovalKey(s.Files.Root)
	if err != nil {
		return ErrApproval
	}
	if strings.TrimSpace(actor) == "" || len(actor) > 128 || !approvalKeyFormat.MatchString(strings.TrimSpace(key)) || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(key)), []byte(secret)) != 1 {
		return ErrApproval
	}
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var b []byte
	if err = tx.QueryRow(ctx, `SELECT payload FROM graph_releases WHERE id=$1 FOR UPDATE`, id).Scan(&b); err != nil {
		return err
	}
	var r Release
	if err = json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.Status != "eligible" || !r.Development.Passed || !r.Audit.Passed {
		return fmt.Errorf("candidate has not passed its frozen evaluation gates")
	}
	if r.AutoReviewID != "" && (r.Regression == nil || !r.Regression.Passed) {
		return fmt.Errorf("automatic revision requires a successful triggering-batch replay")
	}
	// Serialize first promotion and subsequent compare-and-swap by task identity.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, r.Task); err != nil {
		return err
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT release_id FROM graph_channels WHERE task_id=$1 FOR UPDATE`, r.Task).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if current != expected {
		return fmt.Errorf("active release changed; refresh before approving")
	}
	now := time.Now().UTC()
	r.ApprovedAt = &now
	r.ApprovedBy = actor
	if _, err = tx.Exec(ctx, `UPDATE graph_releases SET payload=$2 WHERE id=$1`, id, encode(r)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO graph_channels(task_id,release_id) VALUES($1,$2) ON CONFLICT(task_id) DO UPDATE SET release_id=excluded.release_id,updated_at=now()`, r.Task, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO graph_approvals(task_id,release_id,previous_release_id,actor) VALUES($1,$2,$3,$4)`, r.Task, id, current, actor); err != nil {
		return err
	}
	if r.AutoReviewID != "" {
		if _, err = tx.Exec(ctx, `UPDATE graph_auto_reviews SET status='approved', candidate_id=$2, payload=jsonb_set(jsonb_set(payload,'{status}','"approved"'),'{candidate_id}',to_jsonb($2::text)) WHERE id=$1`, r.AutoReviewID, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_memory(task_id,source,content,dedup_key) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.Task, "human:"+actor, "Human explicitly approved automatic robustness revision "+id+" from review "+r.AutoReviewID+" after frozen evaluation and triggering-batch replay.", r.AutoReviewID+"/approval"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) AdmitBatch(ctx context.Context, in BatchInput) (Batch, bool, error) {
	r, err := s.Release(ctx, in.ReleaseID)
	if err != nil {
		return Batch{}, false, err
	}
	if r.ApprovedAt == nil {
		return Batch{}, false, fmt.Errorf("release requires explicit human approval before batch serving")
	}
	now := time.Now().UTC()
	b := Batch{BatchInput: in, Task: r.Task, Status: "queued", Trace: []execution.NodeResult{}, CreatedAt: now, UpdatedAt: now}
	tag, err := s.DB.Pool.Exec(ctx, `INSERT INTO graph_batches(id,task_id,input_digest,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, in.ID, r.Task, graph.Digest(in), encode(b))
	if err != nil {
		return b, false, err
	}
	if tag.RowsAffected() == 1 {
		return b, true, nil
	}
	var digest string
	var raw []byte
	if err = s.DB.Pool.QueryRow(ctx, `SELECT input_digest,payload FROM graph_batches WHERE id=$1`, in.ID).Scan(&digest, &raw); err != nil {
		return b, false, err
	}
	if digest != graph.Digest(in) {
		return b, false, ErrConflict
	}
	err = json.Unmarshal(raw, &b)
	return b, false, err
}
func (s *Service) Batch(ctx context.Context, id string) (Batch, error) {
	var b Batch
	var raw []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM graph_batches WHERE id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	return b, err
}
func (s *Service) SaveBatch(ctx context.Context, b Batch) error {
	b.UpdatedAt = time.Now().UTC()
	_, err := s.DB.Pool.Exec(ctx, `UPDATE graph_batches SET payload=$2 WHERE id=$1`, b.ID, encode(b))
	return err
}
