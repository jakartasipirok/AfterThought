package pipeline

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/graph"
)

const MaxPolicyPDFBytes = 8 << 20
const maxPolicyTextBytes = 128 << 10

type PolicyPage struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}
type PolicyDocument struct {
	ID        string         `json:"id"`
	Task      string         `json:"task"`
	Title     string         `json:"title"`
	Version   int            `json:"version"`
	ParentID  string         `json:"parent_id,omitempty"`
	PDF       graph.Artifact `json:"pdf"`
	Pages     []PolicyPage   `json:"pages"`
	Synthetic bool           `json:"synthetic"`
	Actor     string         `json:"actor"`
	CreatedAt time.Time      `json:"created_at"`
}
type PolicyCitation struct {
	Page  int    `json:"page"`
	Quote string `json:"quote"`
}
type PolicyDraft struct {
	Title       string           `json:"title"`
	Hypothesis  string           `json:"hypothesis"`
	Rationale   string           `json:"rationale"`
	Uncertainty string           `json:"uncertainty"`
	Amendment   string           `json:"amendment"`
	Citations   []PolicyCitation `json:"citations"`
	EvidenceIDs []string         `json:"evidence_ids"`
}
type PolicyProposal struct {
	ID                string      `json:"id"`
	Task              string      `json:"task"`
	PolicyID          string      `json:"policy_id"`
	SessionID         string      `json:"session_id"`
	Status            string      `json:"status"`
	Draft             PolicyDraft `json:"draft"`
	Verdict           string      `json:"verdict,omitempty"`
	Note              string      `json:"note,omitempty"`
	Actor             string      `json:"actor,omitempty"`
	PublishedPolicyID string      `json:"published_policy_id,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
}

func (s *Service) migratePolicies(ctx context.Context) error {
	_, err := s.DB.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS policy_documents(id text PRIMARY KEY,task_id text NOT NULL,payload jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS policy_channels(task_id text PRIMARY KEY,document_id text NOT NULL REFERENCES policy_documents(id));
CREATE TABLE IF NOT EXISTS policy_proposals(id text PRIMARY KEY,task_id text NOT NULL,payload jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS policy_pattern_decisions(task_id text NOT NULL,policy_id text NOT NULL,pattern_id text NOT NULL,review_id text NOT NULL,PRIMARY KEY(task_id,policy_id,pattern_id));`)
	return err
}

// Policy extraction uses a bounded subprocess and accepts text PDFs up to 30 pages.
type policyTextBuffer struct{ bytes.Buffer }

func (b *policyTextBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxPolicyTextBytes {
		return 0, fmt.Errorf("policy text exceeds 128 KiB")
	}
	return b.Buffer.Write(p)
}
func extractPolicyPDF(ctx context.Context, raw []byte) ([]PolicyPage, error) {
	if len(raw) == 0 || len(raw) > MaxPolicyPDFBytes || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return nil, fmt.Errorf("upload a PDF up to 8 MiB")
	}
	dir, err := os.MkdirTemp("", "afterthought-policy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "source.pdf")
	if err = os.WriteFile(name, raw, 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdftotext", "-f", "1", "-l", "31", "-layout", "-enc", "UTF-8", name, "-")
	var output policyTextBuffer
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("PDF extraction requires Poppler; install it with brew install poppler")
		}
		return nil, fmt.Errorf("could not extract a bounded text PDF; use an unencrypted text PDF up to 30 pages")
	}
	chunks := strings.Split(strings.TrimSuffix(output.String(), "\f"), "\f")
	if len(chunks) > 30 {
		return nil, fmt.Errorf("policy PDF must have at most 30 pages")
	}
	pages := []PolicyPage{}
	for i, text := range chunks {
		if text = strings.TrimSpace(text); text != "" {
			pages = append(pages, PolicyPage{Number: i + 1, Text: text})
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("PDF contains no extractable text; upload a searchable PDF")
	}
	return pages, nil
}

func (s *Service) Policy(ctx context.Context, id string) (PolicyDocument, error) {
	var doc PolicyDocument
	var raw []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM policy_documents WHERE id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	return doc, err
}
func (s *Service) CurrentPolicy(ctx context.Context, task string) (*PolicyDocument, error) {
	var id string
	err := s.DB.Pool.QueryRow(ctx, `SELECT document_id FROM policy_channels WHERE task_id=$1`, task).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := s.Policy(ctx, id)
	return &doc, err
}
func (s *Service) UploadPolicy(ctx context.Context, task, title, actor string, synthetic bool, raw []byte) (PolicyDocument, error) {
	pages, err := extractPolicyPDF(ctx, raw)
	if err != nil {
		return PolicyDocument{}, err
	}
	pdf, err := s.Files.Put(raw)
	if err != nil {
		return PolicyDocument{}, err
	}
	doc := PolicyDocument{ID: "policy-" + strings.TrimPrefix(graph.Digest([]string{task, pdf.Digest}), "sha256:")[:40], Task: task, Title: title, Version: 1, PDF: pdf, Pages: pages, Synthetic: synthetic, Actor: actor, CreatedAt: time.Now().UTC()}
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return doc, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, task); err != nil {
		return doc, err
	}
	var existing []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM policy_documents WHERE id=$1`, doc.ID).Scan(&existing)
	if err == nil {
		err = json.Unmarshal(existing, &doc)
		return doc, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return doc, err
	}
	var parentRaw []byte
	err = tx.QueryRow(ctx, `SELECT d.payload FROM policy_channels c JOIN policy_documents d ON d.id=c.document_id WHERE c.task_id=$1`, task).Scan(&parentRaw)
	if err == nil {
		var parent PolicyDocument
		if err = json.Unmarshal(parentRaw, &parent); err != nil {
			return doc, err
		}
		doc.ParentID, doc.Version = parent.ID, parent.Version+1
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return doc, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO policy_documents(id,task_id,payload) VALUES($1,$2,$3)`, doc.ID, task, encode(doc)); err != nil {
		return doc, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO policy_channels(task_id,document_id) VALUES($1,$2) ON CONFLICT(task_id) DO UPDATE SET document_id=excluded.document_id`, task, doc.ID); err != nil {
		return doc, err
	}
	return doc, tx.Commit(ctx)
}

func (s *Service) PolicyProposal(ctx context.Context, id string) (PolicyProposal, error) {
	var proposal PolicyProposal
	var raw []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT payload FROM policy_proposals WHERE id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &proposal)
	}
	return proposal, err
}

func validatePolicyDraft(d PolicyDraft, doc PolicyDocument, evidence []Novelty) error {
	for _, value := range []string{d.Title, d.Hypothesis, d.Rationale, d.Uncertainty, d.Amendment} {
		if strings.TrimSpace(value) == "" || len(value) > 8000 {
			return fmt.Errorf("proposal requires bounded title, hypothesis, rationale, uncertainty and amendment")
		}
	}
	if len(d.Title) > 200 || len(d.Citations) < 1 || len(d.Citations) > 8 || len(d.EvidenceIDs) < 1 || len(d.EvidenceIDs) > 16 {
		return fmt.Errorf("proposal requires 1..8 policy citations and 1..16 evidence references")
	}
	for _, citation := range d.Citations {
		quote := strings.Join(strings.Fields(citation.Quote), " ")
		if len(quote) < 16 || len(quote) > 500 {
			return fmt.Errorf("policy quotes must contain 16..500 characters")
		}
		found := false
		for _, page := range doc.Pages {
			if page.Number == citation.Page && strings.Contains(strings.Join(strings.Fields(page.Text), " "), quote) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("policy citation must exactly match its source page")
		}
	}
	for _, id := range d.EvidenceIDs {
		found := false
		for _, signal := range evidence {
			if signal.ID == id {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("proposal cited an unknown evidence ID")
		}
	}
	return nil
}

func (s *Service) checkHuman(actor, key string) error {
	secret, err := ReadApprovalKey(s.Files.Root)
	if err != nil || strings.TrimSpace(actor) == "" || len(actor) > 128 || !approvalKeyFormat.MatchString(strings.TrimSpace(key)) || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(key)), []byte(secret)) != 1 {
		return ErrApproval
	}
	return nil
}

// A policy decision publishes knowledge only. Graph release activation continues
// through the existing evaluated-release approval transaction.
func (s *Service) DecidePolicy(ctx context.Context, id, decision, verdict, note, actor, key, expected string) (PolicyProposal, error) {
	if err := s.checkHuman(actor, key); err != nil {
		return PolicyProposal{}, err
	}
	return s.DecidePolicyFromOperator(ctx, id, decision, verdict, note, actor, expected)
}

// DecidePolicyFromOperator records an explicit decision from the local operator UI.
// It is never registered as an agent tool or Activity.
func (s *Service) DecidePolicyFromOperator(ctx context.Context, id, decision, verdict, note, actor, expected string) (PolicyProposal, error) {
	if strings.TrimSpace(actor) == "" || len(actor) > 128 {
		return PolicyProposal{}, fmt.Errorf("a reviewer name is required")
	}
	if decision != "approve" && decision != "dismiss" {
		return PolicyProposal{}, fmt.Errorf("choose approve or dismiss")
	}
	if verdict != "suspected_scam" && verdict != "benign_change" && verdict != "needs_investigation" {
		return PolicyProposal{}, fmt.Errorf("record your judgment of the pattern")
	}
	if strings.TrimSpace(note) == "" || len(note) > 2000 {
		return PolicyProposal{}, fmt.Errorf("add a decision note up to 2000 characters")
	}
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return PolicyProposal{}, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var proposal PolicyProposal
	if err = tx.QueryRow(ctx, `SELECT payload FROM policy_proposals WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
		return proposal, err
	}
	if err = json.Unmarshal(raw, &proposal); err != nil {
		return proposal, err
	}
	if proposal.Status != "awaiting_policy_review" {
		return proposal, fmt.Errorf("this proposal already has a human decision")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, proposal.Task); err != nil {
		return proposal, err
	}
	var current string
	if err = tx.QueryRow(ctx, `SELECT document_id FROM policy_channels WHERE task_id=$1`, proposal.Task).Scan(&current); err != nil {
		return proposal, err
	}
	if decision == "approve" && (current != expected || current != proposal.PolicyID) {
		return proposal, fmt.Errorf("company policy changed; review a proposal against the current version")
	}
	proposal.Status, proposal.Verdict, proposal.Note, proposal.Actor = "dismissed", verdict, note, actor
	if decision == "approve" {
		var parent PolicyDocument
		if err = tx.QueryRow(ctx, `SELECT payload FROM policy_documents WHERE id=$1`, current).Scan(&raw); err != nil {
			return proposal, err
		}
		if err = json.Unmarshal(raw, &parent); err != nil {
			return proposal, err
		}
		doc := parent
		doc.ID = "policy-" + strings.TrimPrefix(graph.Digest([]string{parent.ID, proposal.ID, proposal.Draft.Amendment}), "sha256:")[:40]
		doc.ParentID, doc.Version, doc.Actor, doc.CreatedAt = parent.ID, parent.Version+1, actor, time.Now().UTC()
		doc.Pages = append(append([]PolicyPage{}, parent.Pages...), PolicyPage{Number: parent.Pages[len(parent.Pages)-1].Number + 1, Text: proposal.Draft.Title + "\n\n" + proposal.Draft.Amendment})
		if len(encode(doc.Pages)) > maxPolicyTextBytes {
			return proposal, fmt.Errorf("policy exceeds text budget; upload a consolidated version")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO policy_documents(id,task_id,payload) VALUES($1,$2,$3)`, doc.ID, doc.Task, encode(doc)); err != nil {
			return proposal, err
		}
		if _, err = tx.Exec(ctx, `UPDATE policy_channels SET document_id=$2 WHERE task_id=$1`, doc.Task, doc.ID); err != nil {
			return proposal, err
		}
		proposal.Status, proposal.PublishedPolicyID = "approved", doc.ID
		current = doc.ID
	}
	if _, err = tx.Exec(ctx, `UPDATE policy_proposals SET payload=$2 WHERE id=$1`, id, encode(proposal)); err != nil {
		return proposal, err
	}
	if _, err = tx.Exec(ctx, `UPDATE graph_auto_reviews SET status=$2,payload=jsonb_set(payload,'{status}',to_jsonb($2::text)) WHERE id=$1`, id, proposal.Status); err != nil {
		return proposal, err
	}
	// Remember reviewed signals for this policy version so the same departure
	// does not repeatedly generate amendments after a human decision.
	var review AutoReview
	if err = tx.QueryRow(ctx, `SELECT payload FROM graph_auto_reviews WHERE id=$1`, id).Scan(&raw); err != nil {
		return proposal, err
	}
	if err = json.Unmarshal(raw, &review); err != nil {
		return proposal, err
	}
	for _, signal := range review.Novelty {
		if _, err = tx.Exec(ctx, `INSERT INTO policy_pattern_decisions(task_id,policy_id,pattern_id,review_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, proposal.Task, current, signal.ID, id); err != nil {
			return proposal, err
		}
	}
	evidence := fmt.Sprintf("Human %s recorded %s for emerging-pattern review %s. Judgment=%s. Note=%s. Published policy=%s.", actor, decision, id, verdict, note, proposal.PublishedPolicyID)
	if _, err = tx.Exec(ctx, `INSERT INTO agent_memory(task_id,source,content,dedup_key) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, proposal.Task, "human:"+actor, evidence, id+"/policy-decision"); err != nil {
		return proposal, err
	}
	return proposal, tx.Commit(ctx)
}
