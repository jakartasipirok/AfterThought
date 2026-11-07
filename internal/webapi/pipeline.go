package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/pipeline"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

func (s *Server) pipelineRoutes(m *http.ServeMux) {
	s.policyRoutes(m)
	m.HandleFunc("GET /api/auto-review", s.autoReviewStatus)
	m.HandleFunc("PUT /api/auto-review", s.setAutoReview)
	m.HandleFunc("POST /api/datasets", s.uploadDataset)
	m.HandleFunc("GET /api/datasets", s.datasets)
	m.HandleFunc("GET /api/datasets/{id}", s.dataset)
	m.HandleFunc("POST /api/bootstraps", s.bootstrap)
	m.HandleFunc("GET /api/bootstraps", s.bootstraps)
	m.HandleFunc("GET /api/bootstraps/{id}", s.bootstrapDetail)
	m.HandleFunc("GET /api/releases", s.releases)
	m.HandleFunc("GET /api/releases/{id}", s.release)
	m.HandleFunc("GET /api/releases/{id}/nodes/{phase}/{node}", s.releaseNode)
	m.HandleFunc("POST /api/releases/{id}/tune", s.tune)
	m.HandleFunc("POST /api/releases/{id}/approve", s.approve)
	m.HandleFunc("POST /api/releases/{id}/runs", s.batchStart)
	m.HandleFunc("GET /api/graph-runs/{id}", s.batchDetail)
	m.HandleFunc("GET /api/graph-runs/{id}/output", s.batchOutput)
}
func pipelineError(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, pgx.ErrNoRows) {
		status = 404
		err = fmt.Errorf("record not found")
	}
	if errors.Is(err, pipeline.ErrConflict) {
		status = 409
	}
	if errors.Is(err, pipeline.ErrApproval) {
		status = 403
	}
	failure(w, status, err.Error())
}
func csvForm(w http.ResponseWriter, r *http.Request) (multipart.File, func(), bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		failure(w, 415, "upload a CSV using multipart/form-data")
		return nil, func() {}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, dataset.MaxCSVBytes+(1<<20))
	if err = r.ParseMultipartForm(1 << 20); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		failure(w, 400, "invalid or oversized CSV upload")
		return nil, func() {}, false
	}
	cleanup := func() { _ = r.MultipartForm.RemoveAll() }
	f, _, err := r.FormFile("file")
	if err != nil {
		cleanup()
		failure(w, 400, "a CSV file is required")
		return nil, func() {}, false
	}
	return f, func() { f.Close(); cleanup() }, true
}
func (s *Server) uploadDataset(w http.ResponseWriter, r *http.Request) {
	f, close, ok := csvForm(w, r)
	if !ok {
		return
	}
	defer close()
	var options dataset.CreateOptions
	if err := graph.Decode([]byte(r.FormValue("options")), &options); err != nil {
		failure(w, 400, "options must contain valid dataset metadata JSON")
		return
	}
	if !identifier.MatchString(options.Task) {
		failure(w, 400, "invalid learning scope; use letters, numbers, dots, dashes or underscores")
		return
	}
	d, err := s.Pipeline.Datasets.Create(r.Context(), f, options)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 201, d)
}
func (s *Server) datasets(w http.ResponseWriter, r *http.Request) {
	d, err := s.Pipeline.Datasets.List(r.Context(), r.URL.Query().Get("task"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"datasets": d})
}
func (s *Server) dataset(w http.ResponseWriter, r *http.Request) {
	d, err := s.Pipeline.Datasets.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, d)
}

// All asynchronous admission verifies existing Temporal identity, including
// completed executions; an arbitrary Workflow with a colliding ID is rejected.
func (s *Server) startGraphWorkflow(ctx context.Context, id string, input any, wf any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	digest := graph.Digest(input)
	_, err := s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: s.queue(), WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionErrorWhenAlreadyStarted: true, Memo: map[string]any{"afterthought_input_digest": digest}}, wf, input)
	if err == nil {
		return nil
	}
	var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
	if !errors.As(err, &duplicate) {
		return fmt.Errorf("Temporal submission unavailable; retry with the same request ID")
	}
	description, e := s.Temporal.DescribeWorkflowExecution(ctx, id, duplicate.RunId)
	if e != nil {
		return fmt.Errorf("could not verify existing execution; retry the same ID")
	}
	payload := description.GetWorkflowExecutionInfo().GetMemo().GetFields()["afterthought_input_digest"]
	var actual string
	if payload == nil || converter.GetDefaultDataConverter().FromPayload(payload, &actual) != nil || actual != digest {
		return pipeline.ErrConflict
	}
	return nil
}
func (s *Server) startBuild(w http.ResponseWriter, r *http.Request, in pipeline.BuildInput) {
	if in.AutoReviewID != "" || in.Regression != nil || strings.HasPrefix(in.ID, "auto-review-") {
		failure(w, 400, "automatic review fields and IDs are reserved for the worker")
		return
	}
	if !identifier.MatchString(in.ID) || len(in.ID) > 80 || !identifier.MatchString(in.Task) {
		failure(w, 400, "valid task and request ID (up to80 characters) are required")
		return
	}
	if in.MaxTurns == 0 {
		in.MaxTurns = 20
	}
	if in.MaxTurns < 1 || in.MaxTurns > 32 || len(in.Prompt) > 12000 {
		failure(w, 400, "use a turn budget1..32 and prompt up to12000 bytes")
		return
	}
	if in.PolicyID == "" {
		previous, lookupErr := s.Pipeline.Job(r.Context(), in.ID)
		if lookupErr == nil {
			in.PolicyID = previous.PolicyID
		} else if errors.Is(lookupErr, pgx.ErrNoRows) {
			policy, policyErr := s.Pipeline.CurrentPolicy(r.Context(), in.Task)
			if policyErr != nil {
				pipelineError(w, policyErr)
				return
			}
			if policy != nil {
				in.PolicyID = policy.ID
			}
		} else {
			pipelineError(w, lookupErr)
			return
		}
	}
	if in.PolicyID != "" {
		policy, policyErr := s.Pipeline.Policy(r.Context(), in.PolicyID)
		if policyErr != nil {
			pipelineError(w, policyErr)
			return
		}
		if policy.Task != in.Task {
			failure(w, 400, "policy belongs to a different scope")
			return
		}
	}
	d, err := s.Pipeline.Datasets.Get(r.Context(), in.DatasetID)
	if err != nil {
		pipelineError(w, err)
		return
	}
	if d.Task != in.Task {
		failure(w, 400, "dataset belongs to a different learning scope")
		return
	}
	if in.ParentReleaseID != "" {
		p, err := s.Pipeline.Release(r.Context(), in.ParentReleaseID)
		if err != nil {
			pipelineError(w, err)
			return
		}
		if p.Task != in.Task || p.DatasetID != in.DatasetID {
			failure(w, 400, "tuning must retain the incumbent task and dataset")
			return
		}
		parts := strings.Split(in.NodeID, ":")
		if len(parts) != 2 || (parts[0] != "fit" && parts[0] != "serve") {
			failure(w, 400, "select a node as fit:ID or serve:ID")
			return
		}
		g := p.Graph.Fit
		if parts[0] == "serve" {
			g = p.Graph.Serve
		}
		found := false
		for _, n := range g.Nodes {
			if n.ID == parts[1] {
				found = true
			}
		}
		if !found {
			failure(w, 400, "selected node does not exist")
			return
		}
	}
	j, created, err := s.Pipeline.AdmitBuild(r.Context(), in)
	if err != nil {
		pipelineError(w, err)
		return
	}
	if err = s.startGraphWorkflow(r.Context(), in.ID, in, pipeline.BuildWorkflow); err != nil {
		if errors.Is(err, pipeline.ErrConflict) {
			pipelineError(w, err)
		} else {
			failure(w, 503, err.Error())
		}
		return
	}
	status := 202
	if !created {
		status = 200
	}
	respond(w, status, map[string]any{"id": in.ID, "status": j.Status, "duplicate": !created, "job": j})
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var in pipeline.BuildInput
	if !decode(w, r, &in) {
		return
	}
	s.startBuild(w, r, in)
}
func (s *Server) bootstraps(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Pipeline.Jobs(r.Context(), r.URL.Query().Get("task"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"jobs": jobs})
}
func (s *Server) bootstrapDetail(w http.ResponseWriter, r *http.Request) {
	j, err := s.Pipeline.Job(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	out := map[string]any{"job": j}
	if j.ReleaseID != "" {
		if rel, e := s.Pipeline.Release(r.Context(), j.ReleaseID); e == nil {
			out["release"] = rel
		}
	}
	respond(w, 200, out)
}
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	releases, err := s.Pipeline.Releases(r.Context(), task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	active, err := s.Pipeline.Active(r.Context(), task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"releases": releases, "active_release_id": active})
}
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	rel, err := s.Pipeline.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"release": rel})
}
func (s *Server) releaseNode(w http.ResponseWriter, r *http.Request) {
	rel, err := s.Pipeline.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	phase := r.PathValue("phase")
	if phase != "fit" && phase != "serve" {
		failure(w, 400, "unknown graph phase")
		return
	}
	g := rel.Graph.Fit
	if phase == "serve" {
		g = rel.Graph.Serve
	}
	for _, node := range g.Nodes {
		if node.ID == r.PathValue("node") {
			b, err := s.Files.Get(node.Source)
			if err != nil {
				failure(w, 503, "retained node source is unavailable")
				return
			}
			respond(w, 200, map[string]any{"node": node, "source": string(b)})
			return
		}
	}
	failure(w, 404, "node not found")
}
func (s *Server) tune(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID       string `json:"id"`
		NodeID   string `json:"node_id"`
		Prompt   string `json:"prompt"`
		MaxTurns int    `json:"max_turns"`
	}
	if !decode(w, r, &request) {
		return
	}
	rel, err := s.Pipeline.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	s.startBuild(w, r, pipeline.BuildInput{ID: request.ID, Task: rel.Task, DatasetID: rel.DatasetID, Prompt: request.Prompt, MaxTurns: request.MaxTurns, ParentReleaseID: rel.ID, NodeID: request.NodeID})
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Actor        string `json:"actor"`
		Approved     bool   `json:"approved"`
		ApprovalCode string `json:"approval_code"`
		Expected     string `json:"expected_current_id"`
	}
	if !decode(w, r, &request) {
		return
	}
	if !request.Approved {
		failure(w, 403, "explicit human approval is required")
		return
	}
	var err error
	if request.ApprovalCode != "" {
		err = s.Pipeline.Approve(r.Context(), r.PathValue("id"), request.Actor, request.ApprovalCode, request.Expected)
	} else {
		if !operatorBrowserRequest(w, r) {
			return
		}
		err = s.Pipeline.ApproveFromOperator(r.Context(), r.PathValue("id"), request.Actor, request.Expected)
	}
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"active_release_id": r.PathValue("id"), "approved_by": request.Actor})
}
func (s *Server) batchStart(w http.ResponseWriter, r *http.Request) {
	f, close, ok := csvForm(w, r)
	if !ok {
		return
	}
	defer close()
	var options struct {
		ID string `json:"id"`
	}
	if err := graph.Decode([]byte(r.FormValue("options")), &options); err != nil || !identifier.MatchString(options.ID) || strings.HasPrefix(options.ID, "auto-review-") || len(options.ID) > 100 {
		failure(w, 400, "options must contain a stable request ID")
		return
	}
	rel, err := s.Pipeline.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	if rel.ApprovedAt == nil {
		failure(w, 403, "a human must approve this release before batch serving")
		return
	}
	input, count, err := s.Pipeline.Datasets.InferenceInput(r.Context(), rel.DatasetID, f)
	if err != nil {
		pipelineError(w, err)
		return
	}
	in := pipeline.BatchInput{ID: options.ID, ReleaseID: rel.ID, Input: input, RowCount: count}
	b, created, err := s.Pipeline.AdmitBatch(r.Context(), in)
	if err != nil {
		pipelineError(w, err)
		return
	}
	if err = s.startGraphWorkflow(r.Context(), in.ID, in, pipeline.BatchWorkflow); err != nil {
		if errors.Is(err, pipeline.ErrConflict) {
			pipelineError(w, err)
		} else {
			failure(w, 503, err.Error())
		}
		return
	}
	status := 202
	if !created {
		status = 200
	}
	respond(w, status, map[string]any{"id": in.ID, "status": b.Status, "duplicate": !created})
}
func (s *Server) batchDetail(w http.ResponseWriter, r *http.Request) {
	b, err := s.Pipeline.Batch(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	out := map[string]any{"run": b, "preview": []dataset.Prediction{}}
	if b.Status == "completed" && b.Output.Digest != "" {
		data, e := s.Files.Get(b.Output)
		var predictions dataset.Output
		if e == nil && json.Unmarshal(data, &predictions) == nil {
			if len(predictions.Predictions) > 100 {
				predictions.Predictions = predictions.Predictions[:100]
			}
			out["preview"] = predictions.Predictions
		}
	}
	respond(w, 200, out)
}
func (s *Server) batchOutput(w http.ResponseWriter, r *http.Request) {
	b, err := s.Pipeline.Batch(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	if b.Status != "completed" || b.Output.Digest == "" {
		failure(w, 409, "batch output is not ready")
		return
	}
	data, err := s.Files.Get(b.Output)
	if err != nil {
		failure(w, 503, "batch output unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="predictions.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func (s *Server) autoReviewStatus(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	if !identifier.MatchString(task) {
		failure(w, 400, "valid task is required")
		return
	}
	status, err := s.Pipeline.AutoReviewStatus(r.Context(), task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, status)
}

func (s *Server) setAutoReview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Task    string `json:"task"`
		Enabled *bool  `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !identifier.MatchString(in.Task) || in.Enabled == nil {
		failure(w, 400, "task and explicit enabled boolean are required")
		return
	}
	if err := s.Pipeline.SetAutoReview(r.Context(), in.Task, *in.Enabled); err != nil {
		pipelineError(w, err)
		return
	}
	status, err := s.Pipeline.AutoReviewStatus(r.Context(), in.Task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, status)
}
