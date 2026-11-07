// Package dataset retains immutable CSV inputs, frozen splits, and trusted
// evaluation evidence outside agent workspaces. It contains no domain model.
package dataset

import (
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

const (
	MaxCSVBytes  = 64 << 20
	MaxRows      = 100000
	MaxColumns   = 128
	MaxFeatures  = 64
	MaxCellBytes = 16 << 10
)

type Store struct {
	DB    *storage.DB
	Files *storage.Files
}

type CreateOptions struct {
	EvaluationOnly       bool     `json:"evaluation_only,omitempty"`
	Name                 string   `json:"name"`
	Task                 string   `json:"task"`
	Target               string   `json:"target"`
	PositiveLabel        string   `json:"positive_label"`
	LabelMeaning         string   `json:"label_meaning"`
	FeatureColumns       []string `json:"feature_columns"`
	LabelsVerified       bool     `json:"labels_verified"`
	Synthetic            bool     `json:"synthetic"`
	MinPrecision         float64  `json:"min_precision"`
	MinRecall            float64  `json:"min_recall"`
	MaxFalsePositiveRate float64  `json:"max_false_positive_rate"`
	Threshold            float64  `json:"threshold"`
}

type Policy struct {
	MinPrecision         float64 `json:"min_precision"`
	MinRecall            float64 `json:"min_recall"`
	MaxFalsePositiveRate float64 `json:"max_false_positive_rate"`
	Threshold            float64 `json:"threshold"`
}

type Counts struct {
	Rows     int `json:"rows"`
	Positive int `json:"positive"`
	Negative int `json:"negative"`
}

type LabelCounts struct {
	Positive int `json:"positive"`
	Negative int `json:"negative"`
}

type Field struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Missing int    `json:"missing"`
}

// Profile intentionally excludes row values, identifiers, and label examples.
// PositiveLabel is the operator-declared mapping, not a sampled source value.
type Profile struct {
	Name            string            `json:"name"`
	Rows            int               `json:"rows"`
	SourceColumns   int               `json:"source_columns"`
	SelectedColumns int               `json:"selected_columns"`
	Target          string            `json:"target"`
	PositiveLabel   string            `json:"positive_label"`
	LabelMeaning    string            `json:"label_meaning"`
	LabelsVerified  bool              `json:"labels_verified"`
	Synthetic       bool              `json:"synthetic"`
	Fields          []Field           `json:"fields"`
	LabelCounts     LabelCounts       `json:"label_counts"`
	SplitCounts     map[string]Counts `json:"split_counts"`
}

type Split struct {
	Rows     int            `json:"rows"`
	Positive int            `json:"positive"`
	Negative int            `json:"negative"`
	Input    graph.Artifact `json:"input"`
	Labeled  graph.Artifact `json:"labeled"`
}

type Dataset struct {
	EvaluationOnly bool             `json:"evaluation_only,omitempty"`
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Task           string           `json:"task"`
	Digest         string           `json:"digest"`
	Source         graph.Artifact   `json:"source"`
	Target         string           `json:"target"`
	PositiveLabel  string           `json:"positive_label"`
	LabelMeaning   string           `json:"label_meaning"`
	FeatureColumns []string         `json:"feature_columns"`
	LabelsVerified bool             `json:"labels_verified"`
	Synthetic      bool             `json:"synthetic"`
	Rows           int              `json:"rows"`
	Profile        Profile          `json:"profile"`
	Policy         Policy           `json:"policy"`
	Splits         map[string]Split `json:"splits"`
	CreatedAt      time.Time        `json:"created_at"`
}

// Data is the JSON node input contract. Serving artifacts omit Labels entirely.
type Data struct {
	Rows   []map[string]any `json:"rows"`
	Labels []int            `json:"labels,omitempty"`
	RowIDs []string         `json:"row_ids"`
}

type Prediction struct {
	RowID       string   `json:"row_id"`
	Probability float64  `json:"probability"`
	Decision    string   `json:"decision"`
	Reasons     []string `json:"reasons"`
}

type Output struct {
	Predictions []Prediction `json:"predictions"`
}

type Metrics struct {
	Total             int     `json:"total"`
	TruePositive      int     `json:"true_positive"`
	TrueNegative      int     `json:"true_negative"`
	FalsePositive     int     `json:"false_positive"`
	FalseNegative     int     `json:"false_negative"`
	Precision         float64 `json:"precision"`
	Recall            float64 `json:"recall"`
	F1                float64 `json:"f1"`
	Accuracy          float64 `json:"accuracy"`
	FalsePositiveRate float64 `json:"false_positive_rate"`
	MajorityBaseline  float64 `json:"majority_baseline"`
}

type Report struct {
	DatasetID          string         `json:"dataset_id"`
	DatasetDigest      string         `json:"dataset_digest"`
	Split              string         `json:"split"`
	PredictionArtifact graph.Artifact `json:"prediction_artifact"`
	InputArtifact      graph.Artifact `json:"input_artifact"`
	Policy             Policy         `json:"policy"`
	Metrics            Metrics        `json:"metrics"`
	Passed             bool           `json:"passed"`
	Failures           []string       `json:"failures"`
	Synthetic          bool           `json:"synthetic"`
	LabelsVerified     bool           `json:"labels_verified"`
}
