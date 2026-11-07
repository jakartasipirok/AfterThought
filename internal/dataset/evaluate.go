package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

// Required probability/identity fields cannot silently become zero values when
// generated code omits them or emits null. Unknown metric fields are rejected.
func (p *Prediction) UnmarshalJSON(data []byte) error {
	var fields struct {
		RowID       *string   `json:"row_id"`
		Probability *float64  `json:"probability"`
		Decision    *string   `json:"decision"`
		Reasons     *[]string `json:"reasons"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&fields); err != nil {
		return err
	}
	if fields.RowID == nil || fields.Probability == nil || fields.Decision == nil || fields.Reasons == nil {
		return fmt.Errorf("prediction requires row_id, probability, decision, and reasons")
	}
	*p = Prediction{RowID: *fields.RowID, Probability: *fields.Probability, Decision: *fields.Decision, Reasons: *fields.Reasons}
	return nil
}

func decodeArtifact(data []byte, out any) error {
	if len(data) > MaxCSVBytes {
		return fmt.Errorf("dataset artifact exceeds 64 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("dataset artifact does not satisfy the JSON contract")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("dataset artifact must contain exactly one JSON value")
	}
	return nil
}

func (s *Store) Evaluate(ctx context.Context, id, split string, predictionArtifact graph.Artifact) (Report, error) {
	name, err := canonicalSplit(split)
	if err != nil {
		return Report{}, err
	}
	dataset, err := s.Get(ctx, id)
	if err != nil {
		return Report{}, err
	}
	truthBytes, err := s.Files.Get(dataset.Splits[name].Labeled)
	if err != nil {
		return Report{}, err
	}
	var truth Data
	if err = decodeArtifact(truthBytes, &truth); err != nil {
		return Report{}, err
	}
	predictionBytes, err := s.Files.Get(predictionArtifact)
	if err != nil {
		return Report{}, err
	}
	var output Output
	if err = decodeArtifact(predictionBytes, &output); err != nil {
		return Report{}, err
	}
	report, err := evaluateData(dataset, name, truth, output)
	report.PredictionArtifact = predictionArtifact
	return report, err
}

// ValidateOutput checks an entire serving batch before any HTTP preview is
// truncated. It does not require labels and never accepts model-supplied metrics.
func (s *Store) ValidateOutput(ctx context.Context, inputArtifact, predictionArtifact graph.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.Files == nil {
		return fmt.Errorf("prediction validation requires the artifact store")
	}
	inputBytes, err := s.Files.Get(inputArtifact)
	if err != nil {
		return err
	}
	// A distinct serving envelope rejects even a null or empty labels property.
	var inputs struct {
		Rows   []map[string]any `json:"rows"`
		RowIDs []string         `json:"row_ids"`
	}
	if err = decodeArtifact(inputBytes, &inputs); err != nil {
		return err
	}
	predictionBytes, err := s.Files.Get(predictionArtifact)
	if err != nil {
		return err
	}
	var output Output
	if err = decodeArtifact(predictionBytes, &output); err != nil {
		return err
	}
	return ValidatePredictions(Data{Rows: inputs.Rows, RowIDs: inputs.RowIDs}, output)
}

func ValidatePredictions(inputs Data, output Output) error {
	if len(inputs.RowIDs) == 0 || len(inputs.RowIDs) > MaxRows || len(inputs.Rows) != len(inputs.RowIDs) {
		return fmt.Errorf("prediction inputs require nonempty feature rows with matching row IDs")
	}
	if len(output.Predictions) != len(inputs.RowIDs) {
		return fmt.Errorf("prediction count must exactly match the input rows")
	}
	ids := make(map[string]bool, len(inputs.RowIDs))
	for _, id := range inputs.RowIDs {
		if id == "" || len(id) > 128 || ids[id] {
			return fmt.Errorf("input row IDs must be nonempty, unique, and bounded")
		}
		ids[id] = true
	}
	seen := make(map[string]bool, len(output.Predictions))
	for _, prediction := range output.Predictions {
		if !ids[prediction.RowID] || seen[prediction.RowID] {
			return fmt.Errorf("predictions must cover every input row ID exactly once without extra IDs")
		}
		seen[prediction.RowID] = true
		if math.IsNaN(prediction.Probability) || math.IsInf(prediction.Probability, 0) || prediction.Probability < 0 || prediction.Probability > 1 {
			return fmt.Errorf("prediction probabilities must be finite numbers between 0 and 1")
		}
		if prediction.Decision != "accept" && prediction.Decision != "escalate" && prediction.Decision != "reject" {
			return fmt.Errorf("prediction decision must be accept, escalate, or reject")
		}
		if len(prediction.Reasons) == 0 || len(prediction.Reasons) > 16 {
			return fmt.Errorf("each prediction requires 1..16 nonempty reasons")
		}
		for _, reason := range prediction.Reasons {
			if strings.TrimSpace(reason) == "" || len(reason) > 2000 {
				return fmt.Errorf("prediction reasons must contain 1..2000 bytes each")
			}
		}
	}
	return nil
}

func evaluateData(dataset Dataset, split string, truth Data, output Output) (Report, error) {
	report := Report{DatasetID: dataset.ID, DatasetDigest: dataset.Digest, Split: split, InputArtifact: dataset.Splits[split].Input, Policy: dataset.Policy, Failures: []string{}, Synthetic: dataset.Synthetic, LabelsVerified: dataset.LabelsVerified}
	if !dataset.LabelsVerified || strings.TrimSpace(dataset.LabelMeaning) == "" || len(truth.RowIDs) == 0 || len(truth.Labels) != len(truth.RowIDs) || len(truth.Rows) != len(truth.RowIDs) {
		return report, fmt.Errorf("evaluation requires a nonempty frozen split with verified labels and declared meaning")
	}
	if err := ValidatePredictions(truth, output); err != nil {
		return report, err
	}
	labels := map[string]int{}
	positive, negative := 0, 0
	for i, id := range truth.RowIDs {
		if id == "" || truth.Labels[i] < 0 || truth.Labels[i] > 1 {
			return report, fmt.Errorf("stored evaluation labels or row identities are invalid")
		}
		if _, exists := labels[id]; exists {
			return report, fmt.Errorf("stored evaluation row identities are not unique")
		}
		labels[id] = truth.Labels[i]
		if truth.Labels[i] == 1 {
			positive++
		} else {
			negative++
		}
	}
	if positive == 0 || negative == 0 {
		return report, fmt.Errorf("evaluation split must contain both binary classes")
	}
	metrics := Metrics{Total: len(truth.RowIDs)}
	for _, prediction := range output.Predictions {
		label := labels[prediction.RowID]
		predictedPositive := prediction.Probability >= dataset.Policy.Threshold
		switch {
		case label == 1 && predictedPositive:
			metrics.TruePositive++
		case label == 0 && predictedPositive:
			metrics.FalsePositive++
		case label == 1:
			metrics.FalseNegative++
		default:
			metrics.TrueNegative++
		}
	}
	metrics.Precision = ratio(metrics.TruePositive, metrics.TruePositive+metrics.FalsePositive)
	metrics.Recall = ratio(metrics.TruePositive, metrics.TruePositive+metrics.FalseNegative)
	metrics.Accuracy = ratio(metrics.TruePositive+metrics.TrueNegative, metrics.Total)
	metrics.FalsePositiveRate = ratio(metrics.FalsePositive, metrics.FalsePositive+metrics.TrueNegative)
	if metrics.Precision+metrics.Recall > 0 {
		metrics.F1 = 2 * metrics.Precision * metrics.Recall / (metrics.Precision + metrics.Recall)
	}
	metrics.MajorityBaseline = ratio(max(positive, negative), metrics.Total)
	report.Metrics = metrics
	if metrics.Precision+1e-12 < dataset.Policy.MinPrecision {
		report.Failures = append(report.Failures, "precision is below the frozen minimum")
	}
	if metrics.Recall+1e-12 < dataset.Policy.MinRecall {
		report.Failures = append(report.Failures, "recall is below the frozen minimum")
	}
	if metrics.FalsePositiveRate > dataset.Policy.MaxFalsePositiveRate+1e-12 {
		report.Failures = append(report.Failures, "false positive rate exceeds the frozen maximum")
	}
	if metrics.Accuracy+1e-12 < metrics.MajorityBaseline {
		report.Failures = append(report.Failures, "accuracy is below the held-out majority baseline")
	}
	report.Passed = len(report.Failures) == 0
	return report, nil
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
