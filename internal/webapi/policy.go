package webapi

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/pipeline"
)

func (s *Server) policyRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/policies", s.currentPolicy)
	m.HandleFunc("POST /api/policies", s.uploadPolicy)
	m.HandleFunc("GET /api/policies/{id}/source", s.policySource)
	m.HandleFunc("GET /api/policies/{id}/text", s.policyText)
	m.HandleFunc("GET /api/policy-proposals/{id}", s.policyProposal)
	m.HandleFunc("POST /api/policy-proposals/{id}/decision", s.policyDecision)
	m.HandleFunc("POST /api/auto-review/run", s.reviewNow)
	m.HandleFunc("POST /api/auto-reviews/{id}/draft", s.draftPolicy)
}
func (s *Server) currentPolicy(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	if !identifier.MatchString(task) {
		failure(w, 400, "valid learning scope is required")
		return
	}
	doc, err := s.Pipeline.CurrentPolicy(r.Context(), task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"document": doc})
}
func (s *Server) uploadPolicy(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		failure(w, 415, "upload a PDF using multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pipeline.MaxPolicyPDFBytes+(1<<20))
	if err = r.ParseMultipartForm(1 << 20); err != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		failure(w, 400, "invalid or oversized policy PDF")
		return
	}
	defer r.MultipartForm.RemoveAll()
	var options struct {
		Task      string `json:"task"`
		Title     string `json:"title"`
		Actor     string `json:"actor"`
		Synthetic bool   `json:"synthetic"`
	}
	if err = json.Unmarshal([]byte(r.FormValue("options")), &options); err != nil || !identifier.MatchString(options.Task) || strings.TrimSpace(options.Title) == "" || len(options.Title) > 128 || strings.TrimSpace(options.Actor) == "" || len(options.Actor) > 128 {
		failure(w, 400, "provide task, policy title and uploader name")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		failure(w, 400, "a policy PDF is required")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, pipeline.MaxPolicyPDFBytes+1))
	if err != nil || len(raw) > pipeline.MaxPolicyPDFBytes {
		failure(w, 400, "policy PDF exceeds 8 MiB")
		return
	}
	doc, err := s.Pipeline.UploadPolicy(r.Context(), options.Task, options.Title, options.Actor, options.Synthetic, raw)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 201, map[string]any{"document": doc})
}
func (s *Server) policySource(w http.ResponseWriter, r *http.Request) {
	doc, err := s.Pipeline.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	raw, err := s.Files.Get(doc.PDF)
	if err != nil {
		failure(w, 503, "source PDF unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="company-policy-source.pdf"`)
	_, _ = w.Write(raw)
}
func (s *Server) policyText(w http.ResponseWriter, r *http.Request) {
	doc, err := s.Pipeline.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="company-policy-v%d.md"`, doc.Version))
	_, _ = io.WriteString(w, pipeline.PolicyText(doc))
}
func (s *Server) policyProposal(w http.ResponseWriter, r *http.Request) {
	proposal, err := s.Pipeline.PolicyProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		pipelineError(w, err)
		return
	}
	doc, err := s.Pipeline.Policy(r.Context(), proposal.PolicyID)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"proposal": proposal, "policy": doc})
}
func (s *Server) policyDecision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision     string `json:"decision"`
		Verdict      string `json:"verdict"`
		Note         string `json:"note"`
		Actor        string `json:"actor"`
		ApprovalCode string `json:"approval_code"`
		Expected     string `json:"expected_policy_id"`
		Confirmed    bool   `json:"confirmed"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Confirmed {
		failure(w, 403, "explicit human confirmation is required")
		return
	}
	proposal, err := s.Pipeline.DecidePolicy(r.Context(), r.PathValue("id"), in.Decision, in.Verdict, in.Note, in.Actor, in.ApprovalCode, in.Expected)
	if err != nil {
		pipelineError(w, err)
		return
	}
	respond(w, 200, map[string]any{"proposal": proposal})
}
func (s *Server) reviewNow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Task string `json:"task"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !identifier.MatchString(in.Task) {
		failure(w, 400, "valid learning scope is required")
		return
	}
	review, err := s.Pipeline.ReviewNow(r.Context(), in.Task)
	if err != nil {
		pipelineError(w, err)
		return
	}
	if review == nil {
		failure(w, 409, "Review needs fresh finished batches on an active release. Resolve any pending proposal or wait for its cooldown.")
		return
	}
	// Durable queued work is retried by the worker if this start loses its acknowledgement.
	if review.Status == "queued" {
		_ = s.Pipeline.DispatchAutoReviews(r.Context(), s.queue())
	}
	respond(w, 202, map[string]any{"review": review})
}
func (s *Server) draftPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Requested bool `json:"requested"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Requested {
		failure(w, 400, "explicit draft request is required")
		return
	}
	if err := s.Pipeline.QueuePolicyDraft(r.Context(), r.PathValue("id")); err != nil {
		pipelineError(w, err)
		return
	}
	_ = s.Pipeline.DispatchAutoReviews(r.Context(), s.queue())
	respond(w, 202, map[string]any{"id": r.PathValue("id"), "status": "queued"})
}
