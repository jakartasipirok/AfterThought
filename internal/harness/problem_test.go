package harness

import "testing"

func TestDatasetAdmissionDoesNotAssumeLabelsOrCreateGraph(t *testing.T) {
	p, err := NewProblem("https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data/data", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.DatasetURL != "https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data" || p.Status != "awaiting_discovery" || p.DatasetVersion != "unresolved" || p.Objective != "" {
		t.Fatalf("invented task semantics: %+v", p)
	}
	if _, err = NewProblem("https://user:secret@example.com/data", ""); err == nil {
		t.Fatal("credential-bearing source accepted")
	}
}
