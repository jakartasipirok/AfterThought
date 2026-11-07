package pipeline

import (
	"context"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/dataset"
)

func TestNoveltyRequiresRepeatedSubstantialNumericDeparture(t *testing.T) {
	training := dataset.Data{}
	for i := 0; i < 20; i++ {
		training.Rows = append(training.Rows, map[string]any{"reuse": float64(i % 3), "text": "private record"})
	}
	batch := dataset.Data{}
	for i := 0; i < 30; i++ {
		batch.Rows = append(batch.Rows, map[string]any{"reuse": float64(1)})
	}
	batch.Rows[0]["reuse"], batch.Rows[1]["reuse"] = float64(8), float64(12)
	if got := detectNovelty("dataset", training, batch); len(got) != 0 {
		t.Fatal("two outliers should not trigger", got)
	}
	batch.Rows[2]["reuse"] = float64(9)
	got := detectNovelty("dataset", training, batch)
	if len(got) != 1 || got[0].Count != 3 || got[0].Rows != 30 || got[0].TrainingMax != 2 || got[0].BatchMin != 8 || got[0].BatchMax != 12 {
		t.Fatalf("invalid aggregate: %+v", got)
	}
	original := got[0].ID
	batch.Rows[2]["reuse"] = float64(11)
	if detectNovelty("dataset", training, batch)[0].ID != original {
		t.Fatal("signal identity should be stable for the same direction")
	}
	batch.Rows = append(batch.Rows, map[string]any{"reuse": nil})
	if got := detectNovelty("dataset", training, batch); len(got) != 0 {
		t.Fatal("less than ten percent should not trigger")
	}
	for i := range batch.Rows {
		batch.Rows[i]["reuse"] = math.Inf(1)
	}
	if got := detectNovelty("dataset", training, batch); len(got) != 0 {
		t.Fatal("nonfinite data should be ignored")
	}
}

func TestPolicyCitationsRequireExactPageAndKnownEvidence(t *testing.T) {
	doc := PolicyDocument{Pages: []PolicyPage{{Number: 2, Text: "Retain original documents and record each verification step."}}}
	valid := PolicyDraft{Title: "Review evidence", Hypothesis: "Possible reuse", Rationale: "Unusual count", Uncertainty: "May be a data issue", Amendment: "Ask investigators to verify evidence provenance.", Citations: []PolicyCitation{{Page: 2, Quote: "Retain original documents"}}, EvidenceIDs: []string{"signal"}}
	evidence := []Novelty{{ID: "signal"}}
	if err := validatePolicyDraft(valid, doc, evidence); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PolicyDraft){
		func(d *PolicyDraft) { d.Citations = []PolicyCitation{{Page: 1, Quote: "Retain original documents"}} },
		func(d *PolicyDraft) {
			d.Citations = []PolicyCitation{{Page: 2, Quote: "Invented statement from policy"}}
		},
		func(d *PolicyDraft) { d.EvidenceIDs = []string{"invented"} },
		func(d *PolicyDraft) { d.Uncertainty = "" },
	} {
		d := valid
		change(&d)
		if validatePolicyDraft(d, doc, evidence) == nil {
			t.Fatal("invalid proposal accepted", d)
		}
	}
}

func TestPolicyPDFExtractsPagesAndRejectsInvalidInput(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("requires Poppler")
	}
	raw, err := os.ReadFile("../../frontend/assets/demo-company-policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := extractPolicyPDF(context.Background(), raw)
	if err != nil || len(pages) != 2 || pages[0].Number != 1 || pages[1].Number != 2 || !strings.Contains(pages[0].Text, "Pattern review") {
		t.Fatalf("pages=%+v error=%v", pages, err)
	}
	for _, bad := range [][]byte{[]byte("not a PDF"), []byte("%PDF-1.4 broken"), append([]byte("%PDF-"), make([]byte, MaxPolicyPDFBytes)...)} {
		if _, err := extractPolicyPDF(context.Background(), bad); err == nil {
			t.Fatal("invalid PDF accepted")
		}
	}
	var output policyTextBuffer
	if _, err := output.Write(make([]byte, maxPolicyTextBytes+1)); err == nil {
		t.Fatal("unbounded extraction")
	}
}
