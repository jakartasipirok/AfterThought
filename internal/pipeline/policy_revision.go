package pipeline

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/graph"
)

// PolicyRevision records a post-evaluation operator decision. The original
// release and its rejection evidence remain immutable and queryable.
type PolicyRevision struct {
	SourceReleaseID  string    `json:"source_release_id"`
	Actor            string    `json:"actor"`
	Reason           string    `json:"reason"`
	ChangedAt        time.Time `json:"changed_at"`
	PostEvaluation   bool      `json:"post_evaluation"`
	EvaluationWallMS float64   `json:"evaluation_wall_ms"`
}

func triageRevisionAllowed(r Release) error {
	if r.Status != "rejected" || r.ExecutionError != "" || r.PolicyRevision != nil || r.AutoReviewID != "" {
		return fmt.Errorf("triage policy revision requires an original rejected candidate without execution failures or automatic review")
	}
	failedAccuracy := false
	for _, report := range []dataset.Report{r.Development, r.Audit} {
		if report.PredictionArtifact.Digest == "" || report.Policy.AccuracyMode != "" {
			return fmt.Errorf("candidate must retain original development and audit prediction artifacts")
		}
		for _, failure := range report.Failures {
			if failure != "accuracy is below the held-out majority baseline" {
				return fmt.Errorf("triage revision cannot waive another failure: %s", failure)
			}
			failedAccuracy = true
		}
	}
	if !failedAccuracy {
		return fmt.Errorf("candidate has no majority-accuracy failure to revise")
	}
	return nil
}

// RequalifyTriage is a host-only operator operation, absent from model tools,
// workflows, and HTTP routes. It creates a distinct release and does not approve
// it. Approve still requires the host key and expected-current-release check.
func (s *Service) RequalifyTriage(ctx context.Context, id, actor, key, reason string) (Release, error) {
	secret, err := ReadApprovalKey(s.Files.Root)
	if err != nil || strings.TrimSpace(actor) == "" || len(actor) > 128 ||
		!approvalKeyFormat.MatchString(strings.TrimSpace(key)) ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(key)), []byte(secret)) != 1 {
		return Release{}, ErrApproval
	}
	if len(strings.TrimSpace(reason)) < 20 || len(reason) > 4000 {
		return Release{}, fmt.Errorf("policy revision requires a business rationale of 20..4000 bytes")
	}
	parent, err := s.Release(ctx, id)
	if err != nil {
		return Release{}, err
	}
	if err = triageRevisionAllowed(parent); err != nil {
		return Release{}, err
	}
	started := time.Now()
	dev, err := s.Datasets.EvaluateTriage(ctx, parent.DatasetID, "development", parent.Development.PredictionArtifact)
	if err != nil {
		return Release{}, err
	}
	audit, err := s.Datasets.EvaluateTriage(ctx, parent.DatasetID, "audit", parent.Audit.PredictionArtifact)
	if err != nil {
		return Release{}, err
	}
	// Recompute from pinned artifacts; never flip a stored Passed flag or accept
	// operator-supplied scores. Thresholds and measured predictions cannot change.
	if !dev.Passed || !audit.Passed {
		return Release{}, fmt.Errorf("candidate still fails triage gates: development=%v audit=%v", dev.Failures, audit.Failures)
	}
	r := parent
	r.ID = graph.Digest(map[string]any{"source_release_id": id, "policy": dev.Policy, "actor": actor, "reason": reason})
	r.ParentReleaseID = id
	r.Status = "eligible"
	r.Development, r.Audit = dev, audit
	r.ApprovedAt, r.ApprovedBy = nil, ""
	r.CreatedAt = time.Now().UTC()
	r.PolicyRevision = &PolicyRevision{SourceReleaseID: id, Actor: actor, Reason: reason, ChangedAt: r.CreatedAt, PostEvaluation: true}
	r.PolicyRevision.EvaluationWallMS = float64(time.Since(started)) / float64(time.Millisecond)
	if err = s.SaveRelease(ctx, r); err != nil {
		return Release{}, err
	}
	return s.Release(ctx, r.ID)
}
