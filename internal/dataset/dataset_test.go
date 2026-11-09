package dataset

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/storage"
)

func fixtureOptions() CreateOptions {
	return CreateOptions{Name: "Synthetic binary fixture", Task: "test-task", Target: "outcome", PositiveLabel: "yes", LabelMeaning: "Synthetic label generated solely for dataset tests.", FeatureColumns: []string{"signal", "category", "flag"}, LabelsVerified: true, Synthetic: true, MinPrecision: .8, MinRecall: .8, MaxFalsePositiveRate: .1, Threshold: .5}
}

// The fixture is generated in tests only. No domain node or model is seeded.
func fixtureCSV(t *testing.T, rows int, large bool) []byte {
	t.Helper()
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	if err := w.Write([]string{"signal", "category", "flag", "outcome", "email", "CLAIM_STATUS", "private_note"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		label := "no"
		if i%5 == 0 {
			label = "yes"
		}
		note := "source-only-sentinel"
		if large {
			note += strings.Repeat("x", 400)
		}
		if err := w.Write([]string{strconv.Itoa(i % 100), "category-" + strconv.Itoa(i%3), strconv.FormatBool(i%2 == 0), label, "private@example.test", "historical-disposition", note}); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func prepareFixture(t *testing.T, raw []byte, options CreateOptions) (Dataset, map[string]Data) {
	t.Helper()
	_, table, err := readCSV(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dataset, splits, err := prepare(raw, table, options)
	if err != nil {
		t.Fatal(err)
	}
	return dataset, splits
}

func TestFrozenStratifiedSplitsAndProfilePrivacy(t *testing.T) {
	raw := fixtureCSV(t, 100, false)
	options := fixtureOptions()
	dataset, splits := prepareFixture(t, raw, options)
	again, againSplits := prepareFixture(t, raw, options)
	if dataset.ID != again.ID || dataset.Digest != again.Digest {
		t.Fatal("identical source and frozen policy changed dataset identity")
	}
	first, _ := json.Marshal(splits)
	second, _ := json.Marshal(againSplits)
	if !bytes.Equal(first, second) {
		t.Fatal("stratified splits are not deterministic")
	}
	ids := map[string]bool{}
	for name, data := range splits {
		want := 20
		if name == "train" {
			want = 60
		}
		if len(data.Rows) != want || len(data.Labels) != want || len(data.RowIDs) != want {
			t.Fatalf("unexpected %s split size: %+v", name, dataset.Profile.SplitCounts[name])
		}
		positive := 0
		for i, id := range data.RowIDs {
			if ids[id] {
				t.Fatal("a row leaked across frozen splits")
			}
			ids[id] = true
			positive += data.Labels[i]
			if len(data.Rows[i]) != 3 || data.Rows[i]["outcome"] != nil || data.Rows[i]["email"] != nil || data.Rows[i]["CLAIM_STATUS"] != nil {
				t.Fatal("unselected columns or target leaked into feature rows")
			}
		}
		if positive != want/5 {
			t.Fatalf("%s split lost class stratification: %d/%d", name, positive, want)
		}
	}
	profile, _ := json.Marshal(dataset.Profile)
	for _, excluded := range []string{"private@example.test", "historical-disposition", "source-only-sentinel", "category-1", "email", "CLAIM_STATUS", "private_note"} {
		if bytes.Contains(profile, []byte(excluded)) {
			t.Fatalf("profile exposed a raw or unselected value: %q", excluded)
		}
	}
	if dataset.Profile.Fields[0].Type != "number" || dataset.Profile.Fields[1].Type != "string" || dataset.Profile.Fields[2].Type != "boolean" {
		t.Fatalf("incorrect field types: %+v", dataset.Profile.Fields)
	}
	changed := fixtureOptions()
	changed.Threshold = .6
	candidate, _ := prepareFixture(t, raw, changed)
	if candidate.ID == dataset.ID || candidate.Digest != dataset.Digest {
		t.Fatal("frozen policy was mutable or source identity changed")
	}
	_, changedSplits := prepareFixture(t, raw, changed)
	third, _ := json.Marshal(changedSplits)
	if !bytes.Equal(first, third) {
		t.Fatal("changing policy changed the source-based audit split")
	}
}

func TestDatasetAdmissionRejectsUnverifiedLabelsAndUnsafeFeatures(t *testing.T) {
	raw := fixtureCSV(t, 100, false)
	_, parsed, err := readCSV(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*CreateOptions)
	}{
		{"unverified labels", func(o *CreateOptions) { o.LabelsVerified = false }},
		{"no label meaning", func(o *CreateOptions) { o.LabelMeaning = "" }},
		{"unrecognized positive label", func(o *CreateOptions) { o.PositiveLabel = "other" }},
		{"historical status target", func(o *CreateOptions) { o.Target = "CLAIM_STATUS" }},
		{"historical status feature", func(o *CreateOptions) { o.FeatureColumns = []string{"CLAIM_STATUS"} }},
		{"PII feature", func(o *CreateOptions) { o.FeatureColumns = []string{"email"} }},
		{"target leakage", func(o *CreateOptions) { o.FeatureColumns = []string{"outcome"} }},
		{"duplicate feature", func(o *CreateOptions) { o.FeatureColumns = []string{"signal", "SIGNAL"} }},
		{"missing feature", func(o *CreateOptions) { o.FeatureColumns = []string{"absent"} }},
		{"no selected features", func(o *CreateOptions) { o.FeatureColumns = nil }},
		{"nonfinite gate", func(o *CreateOptions) { o.MinPrecision = math.NaN() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := fixtureOptions()
			test.mutate(&options)
			if _, _, err := prepare(raw, parsed, options); err == nil {
				t.Fatal("invalid dataset was admitted")
			}
		})
	}
	for _, name := range []string{"claimant_name", "SSN", "BANK_ACCOUNT", "phone_number", "home-address", "policy_number", "claim_id", "claim status"} {
		if !forbiddenColumn(name) {
			t.Fatalf("unsafe column %q was not excluded", name)
		}
	}
	short := fixtureCSV(t, 49, false)
	_, table, _ := readCSV(context.Background(), bytes.NewReader(short))
	if _, _, err := prepare(short, table, fixtureOptions()); err == nil {
		t.Fatal("undersized dataset was admitted")
	}
	pii := bytes.ReplaceAll(raw, []byte("category-1"), []byte("person@example.test"))
	_, table, _ = readCSV(context.Background(), bytes.NewReader(pii))
	if _, _, err := prepare(pii, table, fixtureOptions()); err == nil {
		t.Fatal("PII-like feature values were admitted under an innocuous header")
	}
}

func TestCSVBoundsAndMissingLabels(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("x,x,label\n1,2,yes\n"),
		[]byte("x,label\n1,yes,extra\n"),
		[]byte("x,label\n\"unterminated,yes\n"),
		[]byte("x,label\n" + strings.Repeat("a", MaxCellBytes+1) + ",yes\n"),
		{'x', ',', 'y', '\n', 0xff, ',', 'a'},
	} {
		if _, _, err := readCSV(context.Background(), bytes.NewReader(raw)); err == nil {
			t.Fatal("malformed or oversized CSV accepted")
		}
	}
	raw := fixtureCSV(t, 100, false)
	for _, changed := range [][]byte{
		bytes.ReplaceAll(raw, []byte(",yes,"), []byte(",,")),
		bytes.ReplaceAll(raw, []byte(",yes,"), []byte(",no,")),
		bytes.Replace(raw, []byte(",yes,"), []byte(",third,"), 1),
	} {
		_, table, err := readCSV(context.Background(), bytes.NewReader(changed))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = prepare(changed, table, fixtureOptions()); err == nil {
			t.Fatal("missing, one-class, or three-class labels accepted")
		}
	}
}

func TestTrustedBinaryEvaluationAndRowCoverage(t *testing.T) {
	dataset := Dataset{ID: "dataset-test", LabelsVerified: true, LabelMeaning: "Synthetic test class.", Policy: Policy{MinPrecision: .8, MinRecall: .8, MaxFalsePositiveRate: .1, Threshold: .5}, Splits: map[string]Split{"audit": {Rows: 4}}}
	truth := Data{Rows: []map[string]any{{}, {}, {}, {}}, RowIDs: []string{"a", "b", "c", "d"}, Labels: []int{1, 1, 0, 0}}
	output := Output{Predictions: []Prediction{{"a", .9, "escalate", []string{"score"}}, {"b", .4, "accept", []string{"score"}}, {"c", .8, "reject", []string{"score"}}, {"d", .1, "accept", []string{"score"}}}}
	report, err := evaluateData(dataset, "audit", truth, output)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Metrics.TruePositive != 1 || report.Metrics.TrueNegative != 1 || report.Metrics.FalsePositive != 1 || report.Metrics.FalseNegative != 1 || report.Metrics.Precision != .5 || report.Metrics.Recall != .5 || report.Metrics.F1 != .5 || report.Metrics.Accuracy != .5 || report.Metrics.FalsePositiveRate != .5 || report.Metrics.MajorityBaseline != .5 {
		t.Fatalf("backend metrics or frozen gates are wrong: %+v", report)
	}
	perfect := Output{Predictions: []Prediction{{"d", .1, "accept", []string{"score"}}, {"b", .9, "escalate", []string{"score"}}, {"a", .9, "reject", []string{"score"}}, {"c", .1, "accept", []string{"score"}}}}
	report, err = evaluateData(dataset, "audit", truth, perfect)
	if err != nil || !report.Passed || report.Metrics.Accuracy != 1 || report.Metrics.F1 != 1 {
		t.Fatalf("row IDs were not used for evaluation alignment: %+v %v", report, err)
	}
	for _, mutate := range []func(*Output){
		func(o *Output) { o.Predictions = o.Predictions[:3] },
		func(o *Output) { o.Predictions[0].RowID = "a" },
		func(o *Output) { o.Predictions[0].RowID = "unknown" },
		func(o *Output) { o.Predictions[0].Probability = math.NaN() },
		func(o *Output) { o.Predictions[0].Probability = 1.1 },
		func(o *Output) { o.Predictions[0].Reasons = nil },
		func(o *Output) { o.Predictions[0].Reasons = []string{" "} },
		func(o *Output) { o.Predictions[0].Decision = "approve-payment" },
	} {
		copy := Output{Predictions: append([]Prediction(nil), perfect.Predictions...)}
		mutate(&copy)
		if _, err := evaluateData(dataset, "audit", truth, copy); err == nil {
			t.Fatal("malformed prediction coverage or schema was accepted")
		}
	}
	for _, forged := range []string{
		`{"predictions":[],"accuracy":1}`,
		`{"predictions":[{"row_id":"a","decision":"accept","reasons":["x"]}]}`,
		`{"predictions":[{"row_id":"a","probability":null,"decision":"accept","reasons":["x"]}]}`,
		`{"predictions":[{"row_id":"a","probability":0.5,"decision":"accept","reasons":["x"],"correct":true}]}`,
	} {
		var output Output
		if err := decodeArtifact([]byte(forged), &output); err == nil {
			t.Fatal("forged model metrics or missing required prediction fields were accepted")
		}
	}
}

func TestTriagePolicyKeepsAccuracyButStillEnforcesOtherGates(t *testing.T) {
	d := Dataset{LabelsVerified: true, LabelMeaning: "Synthetic fraud triage fixture", Policy: Policy{MinPrecision: .1, MinRecall: .15, MaxFalsePositiveRate: .4, Threshold: .5}}
	truth := Data{}
	output := Output{}
	for i := 0; i < 20; i++ {
		id := strconv.Itoa(i)
		label, probability, decision := 0, .1, "accept"
		if i < 2 {
			label = 1
		}
		if i == 0 || i == 2 || i == 3 {
			probability, decision = .9, "escalate"
		}
		truth.Rows = append(truth.Rows, map[string]any{})
		truth.RowIDs = append(truth.RowIDs, id)
		truth.Labels = append(truth.Labels, label)
		output.Predictions = append(output.Predictions, Prediction{id, probability, decision, []string{"fixture"}})
	}
	original, err := evaluateData(d, "audit", truth, output)
	if err != nil || original.Passed || len(original.Failures) != 1 || original.Metrics.Accuracy != .85 || original.Metrics.MajorityBaseline != .9 {
		t.Fatalf("default accuracy gate changed: %+v %v", original, err)
	}
	d.Policy.AccuracyMode = "diagnostic"
	revised, err := evaluateData(d, "audit", truth, output)
	if err != nil || !revised.Passed || revised.Metrics != original.Metrics {
		t.Fatalf("policy revision changed scores or failed triage gates: %+v %v", revised, err)
	}
	for _, policy := range []Policy{
		{AccuracyMode: "diagnostic", MinPrecision: .8, Threshold: .5, MaxFalsePositiveRate: .4},
		{AccuracyMode: "diagnostic", MinRecall: .8, Threshold: .5, MaxFalsePositiveRate: .4},
		{AccuracyMode: "diagnostic", MaxFalsePositiveRate: .01, Threshold: .5},
	} {
		d.Policy = policy
		report, err := evaluateData(d, "audit", truth, output)
		if err != nil || report.Passed {
			t.Fatalf("triage gate was bypassed: %+v %v", report, err)
		}
	}
	d.Policy.AccuracyMode = "typo"
	if _, err := evaluateData(d, "audit", truth, output); err == nil {
		t.Fatal("unknown policy accepted")
	}
	d.Policy.AccuracyMode = "diagnostic"
	output.Predictions = output.Predictions[:19]
	if _, err := evaluateData(d, "audit", truth, output); err == nil {
		t.Fatal("incomplete predictions accepted")
	}
}

func TestStagedInputsRejectChangesAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = stageFile(root, "input.json", []byte("frozen")); err != nil {
		t.Fatal(err)
	}
	if err = stageFile(root, "input.json", []byte("frozen")); err != nil {
		t.Fatal("identical retry failed", err)
	}
	if err = stageFile(root, "input.json", []byte("changed")); err == nil {
		t.Fatal("changed frozen input silently replaced")
	}
	out := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(out, []byte("frozen"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(out, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err = stageFile(root, "escape", []byte("frozen")); err == nil {
		t.Fatal("staged input followed an outside symlink")
	}
}

func TestLabelFreeBatchValidationChecksEveryPrediction(t *testing.T) {
	files, err := storage.NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Files: files}
	inputs := Data{Rows: make([]map[string]any, 5000), RowIDs: make([]string, 5000)}
	output := Output{Predictions: make([]Prediction, 5000)}
	for i := range inputs.Rows {
		id := "row-" + strconv.Itoa(i)
		inputs.Rows[i], inputs.RowIDs[i] = map[string]any{"signal": i % 3}, id
		output.Predictions[i] = Prediction{RowID: id, Probability: .2, Decision: "accept", Reasons: []string{"Synthetic fixture score."}}
	}
	inputRef, err := store.putJSON(inputs)
	if err != nil {
		t.Fatal(err)
	}
	outputRef, err := store.putJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateOutput(context.Background(), inputRef, outputRef); err != nil {
		t.Fatal("label-free predictions were rejected", err)
	}
	// The final row lies beyond an HTTP preview. Validation must inspect it too.
	output.Predictions[4999].Reasons = nil
	badRef, err := store.putJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateOutput(context.Background(), inputRef, badRef); err == nil {
		t.Fatal("a malformed prediction beyond the preview limit was accepted")
	}
	output.Predictions[4999].Reasons = []string{"Synthetic fixture score."}
	output.Predictions[4999].RowID = output.Predictions[0].RowID
	badRef, err = store.putJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateOutput(context.Background(), inputRef, badRef); err == nil {
		t.Fatal("duplicate ID and missing final row were accepted")
	}
	labeled, err := files.Put([]byte(`{"rows":[{"signal":1}],"row_ids":["row-0"],"labels":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateOutput(context.Background(), labeled, outputRef); err == nil {
		t.Fatal("serving input accepted a labels property")
	}
}

func TestExplicitSplitsKeepChronologyAndMembershipPrivate(t *testing.T) {
	var raw bytes.Buffer
	w := csv.NewWriter(&raw)
	_ = w.Write([]string{"signal", "outcome", "partition"})
	for i := 0; i < 100; i++ {
		name := "train"
		if i >= 60 {
			name = "development"
		}
		if i >= 80 {
			name = "audit"
		}
		label := "no"
		if i%5 == 0 {
			label = "yes"
		}
		_ = w.Write([]string{strconv.Itoa(i), label, name})
	}
	w.Flush()
	options := fixtureOptions()
	options.FeatureColumns = []string{"signal"}
	options.SplitColumn = "partition"
	_, splits := prepareFixture(t, raw.Bytes(), options)
	for name, data := range splits {
		for _, row := range data.Rows {
			i := int(row["signal"].(float64))
			if (name == "train" && i >= 60) || (name == "development" && (i < 60 || i >= 80)) || (name == "audit" && i < 80) {
				t.Fatal("explicit temporal membership changed")
			}
			if len(row) != 1 {
				t.Fatal("split or target leaked into features")
			}
		}
	}
	_, table, err := readCSV(context.Background(), bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"missing", "outcome", "signal"} {
		invalid := options
		invalid.SplitColumn = column
		if _, _, err := prepare(raw.Bytes(), table, invalid); err == nil {
			t.Fatalf("accepted invalid split column %s", column)
		}
	}
	table.rows[0][2] = "other"
	if _, _, err := prepare(raw.Bytes(), table, options); err == nil {
		t.Fatal("accepted unknown split")
	}
}
