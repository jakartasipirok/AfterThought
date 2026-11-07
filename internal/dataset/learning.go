package dataset

import (
	"context"
	"fmt"
	"slices"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

// CompatibleLearning requires identical feature order/types and label semantics.
func CompatibleLearning(base, next Dataset) error {
	if next.Task != base.Task || next.Target != base.Target || next.PositiveLabel != base.PositiveLabel || next.Synthetic != base.Synthetic || !next.LabelsVerified || !slices.Equal(base.FeatureColumns, next.FeatureColumns) || len(base.Profile.Fields) != len(next.Profile.Fields) {
		return fmt.Errorf("learning datasets must share scope, label mapping, provenance type and features")
	}
	for i, field := range base.Profile.Fields {
		if field.Name != next.Profile.Fields[i].Name || field.Type != next.Profile.Fields[i].Type {
			return fmt.Errorf("learning feature types must match the original dataset")
		}
	}
	return nil
}

func (s *Store) labeledData(d Dataset, split string) (Data, error) {
	b, err := s.Files.Get(d.Splits[split].Labeled)
	var data Data
	if err == nil {
		err = decodeArtifact(b, &data)
	}
	return data, err
}

// ValidateLearning keeps all original and supplemental audit rows outside the
// cumulative training/development inputs, including duplicates with new IDs.
func (s *Store) ValidateLearning(ctx context.Context, id string, feedback, backtests []string) error {
	if len(feedback) > 8 || len(backtests) > 4 {
		return fmt.Errorf("use at most eight learning datasets and four backtests")
	}
	base, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if base.EvaluationOnly {
		return fmt.Errorf("evaluation-only data cannot bootstrap a graph")
	}
	learning := []Dataset{base}
	heldout := []Dataset{base}
	seen := map[string]bool{id: true}
	for _, datasetID := range append(slices.Clone(feedback), backtests...) {
		if seen[datasetID] {
			return fmt.Errorf("learning and backtest dataset IDs must be distinct")
		}
		seen[datasetID] = true
		d, e := s.Get(ctx, datasetID)
		if e != nil {
			return e
		}
		if e = CompatibleLearning(base, d); e != nil {
			return e
		}
		heldout = append(heldout, d)
		if slices.Contains(feedback, datasetID) {
			if d.EvaluationOnly {
				return fmt.Errorf("reserved evaluation data cannot be used as feedback")
			}
			learning = append(learning, d)
		}
	}
	// Preserve legacy intake behavior for ordinary builds without extra datasets.
	if len(feedback)+len(backtests) == 0 {
		return nil
	}
	reserved := map[string]bool{}
	for _, d := range heldout {
		data, e := s.labeledData(d, "audit")
		if e != nil {
			return e
		}
		for _, row := range data.Rows {
			reserved[graph.Digest(row)] = true
		}
	}
	seenLearning := map[string]bool{}
	for _, d := range learning {
		for _, split := range []string{"train", "development"} {
			data, e := s.labeledData(d, split)
			if e != nil {
				return e
			}
			for _, row := range data.Rows {
				key := graph.Digest(row)
				if reserved[key] || seenLearning[key] {
					return fmt.Errorf("learning rows overlap another split; deduplicate before splitting")
				}
				seenLearning[key] = true
			}
		}
	}
	return nil
}

// LearningInput merges only the matching train or development splits. The
// original evaluation reports continue to use the original frozen splits.
func (s *Store) LearningInput(ctx context.Context, id string, feedback []string, split string) (graph.Artifact, error) {
	if split != "train" && split != "development" {
		return graph.Artifact{}, fmt.Errorf("learning inputs only support train/development")
	}
	if len(feedback) == 0 {
		return s.Inputs(ctx, id, split, true)
	}
	base, err := s.Get(ctx, id)
	if err != nil {
		return graph.Artifact{}, err
	}
	var combined Data
	seen := map[string]bool{}
	for _, datasetID := range append([]string{id}, feedback...) {
		d, e := s.Get(ctx, datasetID)
		if e != nil {
			return graph.Artifact{}, e
		}
		if d.EvaluationOnly {
			return graph.Artifact{}, fmt.Errorf("reserved data cannot train a graph")
		}
		if e = CompatibleLearning(base, d); e != nil {
			return graph.Artifact{}, e
		}
		data, e := s.labeledData(d, split)
		if e != nil {
			return graph.Artifact{}, e
		}
		for i, row := range data.Rows {
			key := graph.Digest(row)
			if seen[key] {
				return graph.Artifact{}, fmt.Errorf("duplicate learning rows require deduplication")
			}
			seen[key] = true
			combined.Rows = append(combined.Rows, row)
			combined.Labels = append(combined.Labels, data.Labels[i])
			combined.RowIDs = append(combined.RowIDs, data.RowIDs[i])
		}
	}
	if len(combined.Rows) > MaxRows {
		return graph.Artifact{}, fmt.Errorf("cumulative learning input exceeds row limit")
	}
	return s.putJSON(combined)
}
