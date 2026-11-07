package dataset

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

var (
	headerPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_ .-]{0,63}$`)
	taskPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	emailPattern  = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	ssnPattern    = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)
)

type table struct {
	headers []string
	rows    [][]string
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func readCSV(ctx context.Context, input io.Reader) ([]byte, table, error) {
	var out table
	if input == nil {
		return nil, out, fmt.Errorf("CSV reader is required")
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: input}, MaxCSVBytes+1))
	if err != nil {
		return nil, out, fmt.Errorf("could not read CSV: %w", err)
	}
	if len(raw) > MaxCSVBytes {
		return nil, out, fmt.Errorf("CSV exceeds the 64 MiB limit")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, out, fmt.Errorf("CSV must contain UTF-8 text without NUL bytes")
	}
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil || len(header) < 1 || len(header) > MaxColumns {
		return nil, out, fmt.Errorf("CSV requires 1..%d header columns", MaxColumns)
	}
	seen := map[string]bool{}
	for i, name := range header {
		name = strings.TrimSpace(name)
		if i == 0 {
			name = strings.TrimPrefix(name, "\ufeff")
		}
		key := strings.ToLower(name)
		if !headerPattern.MatchString(name) || seen[key] {
			return nil, out, fmt.Errorf("CSV headers must be distinct field names of at most 64 ASCII characters")
		}
		seen[key], header[i] = true, name
	}
	out.headers = header
	for {
		if err := ctx.Err(); err != nil {
			return nil, out, err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) != len(header) {
			return nil, out, fmt.Errorf("CSV row %d is malformed or has a different column count", len(out.rows)+2)
		}
		if len(out.rows) >= MaxRows {
			return nil, out, fmt.Errorf("CSV exceeds %d rows", MaxRows)
		}
		for i, value := range row {
			if len(value) > MaxCellBytes {
				return nil, out, fmt.Errorf("CSV row %d contains a cell larger than %d bytes", len(out.rows)+2, MaxCellBytes)
			}
			row[i] = strings.TrimSpace(value)
		}
		out.rows = append(out.rows, row)
	}
	if len(out.rows) == 0 {
		return nil, out, fmt.Errorf("CSV contains no data rows")
	}
	return raw, out, nil
}

func normalizedName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, name)
}

// This deliberately conservative feature boundary prevents direct identifiers
// and historical disposition codes from becoming authoring or serving inputs.
func forbiddenColumn(name string) bool {
	n := normalizedName(name)
	if n == "claimstatus" || n == "id" || n == "identifier" || strings.HasSuffix(n, "uuid") {
		return true
	}
	for _, token := range []string{"name", "ssn", "socialsecurity", "address", "email", "phone", "telephone", "mobile", "passport", "nationalid", "taxid", "bank", "accountnumber", "routingnumber", "creditcard", "debitcard", "dateofbirth", "birthdate", "dob", "driverlicense", "drivinglicense", "licensenumber", "licenseplate", "registrationnumber", "postcode", "zipcode"} {
		if strings.Contains(n, token) {
			return true
		}
	}
	for _, token := range []string{"claimid", "claimnumber", "policyid", "policynumber", "customerid", "customeridentifier", "personid", "userid", "insuredid", "claimantid", "agentid", "vendorid", "vehicleid", "vin"} {
		if n == token {
			return true
		}
	}
	return false
}

func missing(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "null", "nan", "na", "n/a", "?":
		return true
	}
	return false
}

func optionsFor(t table, options CreateOptions) (CreateOptions, Policy, []int, int, error) {
	options.FeatureColumns = append([]string(nil), options.FeatureColumns...)
	options.Name = strings.TrimSpace(options.Name)
	options.Task = strings.TrimSpace(options.Task)
	options.Target = strings.TrimSpace(options.Target)
	options.PositiveLabel = strings.TrimSpace(options.PositiveLabel)
	options.LabelMeaning = strings.TrimSpace(options.LabelMeaning)
	var policy Policy
	if options.Name == "" || len(options.Name) > 128 || !taskPattern.MatchString(options.Task) {
		return options, policy, nil, 0, fmt.Errorf("dataset requires a name (1..128 bytes) and a task ID")
	}
	if !options.LabelsVerified || options.LabelMeaning == "" || len(options.LabelMeaning) > 2000 || options.PositiveLabel == "" || len(options.PositiveLabel) > 128 {
		return options, policy, nil, 0, fmt.Errorf("declare verified binary labels, their meaning, and the positive label before authoring; synthetic labels must be marked synthetic")
	}
	if forbiddenColumn(options.Target) {
		return options, policy, nil, 0, fmt.Errorf("target cannot be a direct identifier, PII column, or CLAIM_STATUS")
	}
	if len(options.FeatureColumns) < 1 || len(options.FeatureColumns) > MaxFeatures {
		return options, policy, nil, 0, fmt.Errorf("select 1..%d non-PII feature columns explicitly", MaxFeatures)
	}
	byName := map[string]int{}
	for i, name := range t.headers {
		byName[strings.ToLower(name)] = i
	}
	target, ok := byName[strings.ToLower(options.Target)]
	if !ok {
		return options, policy, nil, 0, fmt.Errorf("selected target is absent from the CSV header")
	}
	options.Target = t.headers[target]
	indices := make([]int, 0, len(options.FeatureColumns))
	seen := map[int]bool{}
	for i, feature := range options.FeatureColumns {
		feature = strings.TrimSpace(feature)
		index, ok := byName[strings.ToLower(feature)]
		if !ok || index == target || seen[index] || forbiddenColumn(feature) {
			return options, policy, nil, 0, fmt.Errorf("selected features must exist, be distinct, exclude the target, and exclude PII, identifiers and CLAIM_STATUS")
		}
		indices = append(indices, index)
		seen[index] = true
		options.FeatureColumns[i] = t.headers[index]
	}
	if options.MinPrecision == 0 && options.MinRecall == 0 && options.MaxFalsePositiveRate == 0 {
		options.MinPrecision, options.MinRecall, options.MaxFalsePositiveRate = .7, .7, .2
	}
	if options.Threshold == 0 {
		options.Threshold = .5
	}
	policy = Policy{MinPrecision: options.MinPrecision, MinRecall: options.MinRecall, MaxFalsePositiveRate: options.MaxFalsePositiveRate, Threshold: options.Threshold}
	for _, value := range []float64{policy.MinPrecision, policy.MinRecall, policy.MaxFalsePositiveRate, policy.Threshold} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return options, policy, nil, 0, fmt.Errorf("evaluation thresholds must be finite numbers between 0 and 1")
		}
	}
	return options, policy, indices, target, nil
}

func inferFields(t table, indices []int) []Field {
	fields := make([]Field, len(indices))
	for i, index := range indices {
		field := Field{Name: t.headers[index], Type: "string"}
		numeric, boolean, present := true, true, false
		for _, row := range t.rows {
			value := row[index]
			if missing(value) {
				field.Missing++
				continue
			}
			present = true
			number, err := strconv.ParseFloat(value, 64)
			numeric = numeric && err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
			boolean = boolean && (strings.EqualFold(value, "true") || strings.EqualFold(value, "false"))
		}
		if present && numeric {
			field.Type = "number"
		} else if present && boolean {
			field.Type = "boolean"
		}
		fields[i] = field
	}
	return fields
}

func selectedRow(row []string, indices []int, fields []Field) (map[string]any, error) {
	selected := make(map[string]any, len(indices))
	for i, index := range indices {
		value := row[index]
		if missing(value) {
			selected[fields[i].Name] = nil
			continue
		}
		if emailPattern.MatchString(value) || ssnPattern.MatchString(value) {
			return nil, fmt.Errorf("a selected feature contains a possible personal identifier; remove that feature")
		}
		switch fields[i].Type {
		case "number":
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("a selected numeric feature has an incompatible value")
			}
			selected[fields[i].Name] = number
		case "boolean":
			if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
				return nil, fmt.Errorf("a selected boolean feature has an incompatible value")
			}
			selected[fields[i].Name] = strings.EqualFold(value, "true")
		default:
			selected[fields[i].Name] = value
		}
	}
	return selected, nil
}

func rowID(digest string, index int) string {
	return "row-" + strings.TrimPrefix(graph.Hash([]byte(digest+"/"+strconv.Itoa(index))), "sha256:")
}

func prepare(raw []byte, t table, supplied CreateOptions) (Dataset, map[string]Data, error) {
	var dataset Dataset
	options, policy, indices, target, err := optionsFor(t, supplied)
	if err != nil {
		return dataset, nil, err
	}
	if len(t.rows) < 50 {
		return dataset, nil, fmt.Errorf("at least 50 labeled rows are required for frozen train/development/audit splits")
	}
	digest := graph.Hash(raw)
	fields := inferFields(t, indices)
	all := Data{Rows: make([]map[string]any, len(t.rows)), Labels: make([]int, len(t.rows)), RowIDs: make([]string, len(t.rows))}
	classes := [2][]int{}
	negatives := map[string]bool{}
	for i, row := range t.rows {
		value := row[target]
		if missing(value) {
			return dataset, nil, fmt.Errorf("every training row must have a nonempty verified binary label")
		}
		if value == options.PositiveLabel {
			all.Labels[i] = 1
		} else {
			negatives[value] = true
		}
		if len(negatives) > 1 {
			return dataset, nil, fmt.Errorf("target must contain exactly two classes with the declared positive label")
		}
		selected, err := selectedRow(row, indices, fields)
		if err != nil {
			return dataset, nil, err
		}
		all.Rows[i], all.RowIDs[i] = selected, rowID(digest, i)
		classes[all.Labels[i]] = append(classes[all.Labels[i]], i)
	}
	if len(classes[0]) < 10 || len(classes[1]) < 10 || len(negatives) != 1 {
		return dataset, nil, fmt.Errorf("both verified classes require at least 10 rows so every frozen split has examples")
	}
	splitIndices := map[string][]int{"train": {}, "development": {}, "audit": {}}
	score := func(i int) string { return graph.Hash([]byte(digest + "/split/" + all.RowIDs[i])) }
	for _, class := range classes {
		sort.Slice(class, func(i, j int) bool { return score(class[i]) < score(class[j]) })
		train, development := len(class)*60/100, len(class)*20/100
		if options.EvaluationOnly {
			train, development = 0, 0
		}
		splitIndices["train"] = append(splitIndices["train"], class[:train]...)
		splitIndices["development"] = append(splitIndices["development"], class[train:train+development]...)
		splitIndices["audit"] = append(splitIndices["audit"], class[train+development:]...)
	}
	splits := map[string]Data{}
	counts := map[string]Counts{}
	for _, name := range []string{"train", "development", "audit"} {
		indices := splitIndices[name]
		sort.Slice(indices, func(i, j int) bool { return score(indices[i]) < score(indices[j]) })
		data := Data{Rows: make([]map[string]any, 0, len(indices)), Labels: make([]int, 0, len(indices)), RowIDs: make([]string, 0, len(indices))}
		count := Counts{Rows: len(indices)}
		for _, index := range indices {
			data.Rows = append(data.Rows, all.Rows[index])
			data.Labels = append(data.Labels, all.Labels[index])
			data.RowIDs = append(data.RowIDs, all.RowIDs[index])
			if all.Labels[index] == 1 {
				count.Positive++
			} else {
				count.Negative++
			}
		}
		splits[name], counts[name] = data, count
	}
	identity := graph.Digest(struct {
		Version string        `json:"version"`
		Digest  string        `json:"digest"`
		Options CreateOptions `json:"options"`
	}{"v1", digest, options})
	dataset = Dataset{EvaluationOnly: options.EvaluationOnly, ID: "dataset-" + strings.TrimPrefix(identity, "sha256:")[:24], Name: options.Name, Task: options.Task, Digest: digest, Target: options.Target, PositiveLabel: options.PositiveLabel, LabelMeaning: options.LabelMeaning, FeatureColumns: append([]string(nil), options.FeatureColumns...), LabelsVerified: options.LabelsVerified, Synthetic: options.Synthetic, Rows: len(t.rows), Policy: policy, Splits: map[string]Split{}}
	dataset.Profile = Profile{Name: dataset.Name, Rows: dataset.Rows, SourceColumns: len(t.headers), SelectedColumns: len(fields), Target: dataset.Target, PositiveLabel: dataset.PositiveLabel, LabelMeaning: dataset.LabelMeaning, LabelsVerified: dataset.LabelsVerified, Synthetic: dataset.Synthetic, Fields: fields, LabelCounts: LabelCounts{Positive: len(classes[1]), Negative: len(classes[0])}, SplitCounts: counts}
	return dataset, splits, nil
}

func canonicalSplit(split string) (string, error) {
	switch split {
	case "train", "audit":
		return split, nil
	case "dev", "development":
		return "development", nil
	default:
		return "", fmt.Errorf("split must be train, development, or audit")
	}
}
