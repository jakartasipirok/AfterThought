package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/pipeline"
)

func TestIntegrationBrowserApprovalWithoutHostCode(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("set AFTERTHOUGHT_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, files, temporal, _, _ := integrationServices(t, ctx)
	svc := &pipeline.Service{DB: db, Files: files, Temporal: temporal, Datasets: &dataset.Store{DB: db, Files: files}}
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// These records are isolated test fixtures. No authoring or live approvals run.
	release := pipeline.Release{ID: "candidate", Task: "approval-test", Status: "eligible", Development: dataset.Report{Passed: true}, Audit: dataset.Report{Passed: true}}
	payload, _ := json.Marshal(release)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO graph_releases(id,task_id,payload) VALUES($1,$2,$3)`, release.ID, release.Task, payload); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Pipeline: svc, Files: files}).Handler()
	post := func(path, body, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"actor":"fixture-reviewer","approved":true,"expected_current_id":""}`
	if w := post("/api/releases/candidate/approve", body, ""); w.Code != 403 {
		t.Fatalf("missing origin accepted: %s", w.Body.String())
	}
	if active, err := svc.Active(ctx, release.Task); err != nil || active != "" {
		t.Fatalf("activated before human request: %s %v", active, err)
	}
	if w := post("/api/releases/candidate/approve", body, "http://127.0.0.1:8080"); w.Code != 200 {
		t.Fatalf("browser approval failed: %d %s", w.Code, w.Body.String())
	}
	if active, err := svc.Active(ctx, release.Task); err != nil || active != release.ID {
		t.Fatalf("activation lost: %s %v", active, err)
	}
	if w := post("/api/releases/candidate/approve", body, "http://127.0.0.1:8080"); w.Code != 400 {
		t.Fatalf("stale pointer accepted: %s", w.Body.String())
	}
	var approvals int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM graph_approvals WHERE actor='fixture-reviewer'`).Scan(&approvals); err != nil || approvals != 1 {
		t.Fatalf("approval journal %d %v", approvals, err)
	}

	doc := pipeline.PolicyDocument{ID: "policy", Task: release.Task, Version: 1, Pages: []pipeline.PolicyPage{{Number: 1, Text: "Verify evidence."}}}
	proposal := pipeline.PolicyProposal{ID: "proposal", Task: release.Task, PolicyID: doc.ID, Status: "awaiting_policy_review", Draft: pipeline.PolicyDraft{Title: "Evidence provenance", Amendment: "Check original supporting evidence."}}
	review := pipeline.AutoReview{ID: proposal.ID, Task: release.Task, ParentID: release.ID, Status: proposal.Status, Kind: "emerging_pattern"}
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO policy_documents(id,task_id,payload) VALUES($1,$2,$3)`, []any{doc.ID, doc.Task, doc}},
		{`INSERT INTO policy_proposals(id,task_id,payload) VALUES($1,$2,$3)`, []any{proposal.ID, proposal.Task, proposal}},
		{`INSERT INTO graph_auto_reviews(id,task_id,parent_id,status,payload) VALUES($1,$2,$3,$4,$5)`, []any{review.ID, review.Task, review.ParentID, review.Status, review}},
	} {
		last := len(row.args) - 1
		row.args[last], _ = json.Marshal(row.args[last])
		if _, err := db.Pool.Exec(ctx, row.query, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO policy_channels(task_id,document_id) VALUES($1,$2)`, doc.Task, doc.ID); err != nil {
		t.Fatal(err)
	}
	decision := `{"decision":"approve","verdict":"needs_investigation","note":"Verify originals","actor":"fixture-reviewer","confirmed":true,"expected_policy_id":"policy"}`
	if w := post("/api/policy-proposals/proposal/decision", decision, "http://127.0.0.1:8080"); w.Code != 200 {
		t.Fatalf("policy approval failed: %d %s", w.Code, w.Body.String())
	}
	current, err := svc.CurrentPolicy(ctx, doc.Task)
	if err != nil || current.Version != 2 || current.Actor != "fixture-reviewer" {
		t.Fatalf("policy decision lost: %+v %v", current, err)
	}
}
