package pipeline

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"

	"github.com/jakartasipirok/AfterThought/internal/dataset"
	"github.com/jakartasipirok/AfterThought/internal/graph"
)

// Novelty describes a numerical departure from the frozen training distribution.
// It is evidence for investigation; confirmation belongs to a human reviewer.
type Novelty struct {
	ID          string  `json:"id"`
	Feature     string  `json:"feature"`
	Direction   string  `json:"direction"`
	Count       int     `json:"count"`
	Rows        int     `json:"rows"`
	TrainingMin float64 `json:"training_min"`
	TrainingMax float64 `json:"training_max"`
	BatchMin    float64 `json:"batch_min"`
	BatchMax    float64 `json:"batch_max"`
}

func detectNovelty(datasetID string, training, batch dataset.Data) []Novelty {
	type bounds struct {
		min, max float64
		count    int
	}
	baseline := map[string]bounds{}
	for _, row := range training.Rows {
		for field, value := range row {
			number, ok := value.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				continue
			}
			bound := baseline[field]
			if bound.count == 0 {
				bound.min, bound.max = number, number
			}
			bound.min, bound.max = math.Min(bound.min, number), math.Max(bound.max, number)
			bound.count++
			baseline[field] = bound
		}
	}
	signals := []Novelty{}
	for _, field := range slices.Sorted(maps.Keys(baseline)) {
		bound := baseline[field]
		if bound.count < 10 {
			continue
		}
		margin := math.Max((bound.max-bound.min)*0.25, math.Max(math.Abs(bound.max)*0.01, 0.01))
		for _, direction := range []string{"above_training_range", "below_training_range"} {
			signal := Novelty{ID: graph.Digest([]string{datasetID, field, direction}), Feature: field, Direction: direction, Rows: len(batch.Rows), TrainingMin: bound.min, TrainingMax: bound.max}
			for _, row := range batch.Rows {
				value, ok := row[field].(float64)
				if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
					continue
				}
				outside := direction == "above_training_range" && value > bound.max+margin || direction == "below_training_range" && value < bound.min-margin
				if !outside {
					continue
				}
				if signal.Count == 0 {
					signal.BatchMin, signal.BatchMax = value, value
				}
				signal.Count++
				signal.BatchMin, signal.BatchMax = math.Min(signal.BatchMin, value), math.Max(signal.BatchMax, value)
			}
			if signal.Count >= 3 && float64(signal.Count) >= float64(signal.Rows)*0.1 {
				signals = append(signals, signal)
			}
		}
	}
	return signals
}

func (s *Service) batchNovelty(ctx context.Context, b Batch, data dataset.Data) ([]Novelty, error) {
	release, err := s.Release(ctx, b.ReleaseID)
	if err != nil {
		return nil, err
	}
	frozen, err := s.Datasets.Get(ctx, release.DatasetID)
	if err != nil {
		return nil, err
	}
	raw, err := s.Files.Get(frozen.Splits["train"].Input)
	if err != nil {
		return nil, err
	}
	var training dataset.Data
	if err = json.Unmarshal(raw, &training); err != nil {
		return nil, err
	}
	return detectNovelty(frozen.ID, training, data), nil
}
