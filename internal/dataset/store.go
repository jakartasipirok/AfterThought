package dataset

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

//go:embed schema.sql
var schema string

func (s *Store) configured() error {
	if s == nil || s.DB == nil || s.DB.Pool == nil || s.Files == nil {
		return fmt.Errorf("dataset store requires the database and immutable artifact store")
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	_, err := s.DB.Pool.Exec(ctx, schema)
	return err
}

func (s *Store) Create(ctx context.Context, input io.Reader, options CreateOptions) (Dataset, error) {
	if err := s.configured(); err != nil {
		return Dataset{}, err
	}
	raw, table, err := readCSV(ctx, input)
	if err != nil {
		return Dataset{}, err
	}
	dataset, splits, err := prepare(raw, table, options)
	if err != nil {
		return Dataset{}, err
	}
	if dataset.Source, err = s.Files.Put(raw); err != nil {
		return Dataset{}, fmt.Errorf("could not retain source CSV: %w", err)
	}
	for _, name := range []string{"train", "development", "audit"} {
		data := splits[name]
		labeled, err := s.putJSON(data)
		if err != nil {
			return Dataset{}, fmt.Errorf("could not retain labeled %s split: %w", name, err)
		}
		data.Labels = nil
		input, err := s.putJSON(data)
		if err != nil {
			return Dataset{}, fmt.Errorf("could not retain label-free %s split: %w", name, err)
		}
		counts := dataset.Profile.SplitCounts[name]
		dataset.Splits[name] = Split{Rows: counts.Rows, Positive: counts.Positive, Negative: counts.Negative, Input: input, Labeled: labeled}
	}
	dataset.CreatedAt = time.Now().UTC()
	metadata, err := json.Marshal(dataset)
	if err != nil {
		return Dataset{}, err
	}
	if _, err = s.DB.Pool.Exec(ctx, `INSERT INTO agent_datasets(id,task_id,metadata,created_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, dataset.ID, dataset.Task, metadata, dataset.CreatedAt); err != nil {
		return Dataset{}, fmt.Errorf("could not journal dataset metadata")
	}
	return s.Get(ctx, dataset.ID)
}

func (s *Store) putJSON(value any) (graph.Artifact, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return graph.Artifact{}, err
	}
	if len(data) > MaxCSVBytes {
		return graph.Artifact{}, fmt.Errorf("serialized dataset artifact exceeds 64 MiB")
	}
	return s.Files.Put(data)
}

func (s *Store) Get(ctx context.Context, id string) (Dataset, error) {
	var dataset Dataset
	if err := s.configured(); err != nil {
		return dataset, err
	}
	var metadata []byte
	if err := s.DB.Pool.QueryRow(ctx, `SELECT metadata FROM agent_datasets WHERE id=$1`, id).Scan(&metadata); err != nil {
		return dataset, err
	}
	if err := graph.Decode(metadata, &dataset); err != nil {
		return dataset, fmt.Errorf("stored dataset metadata is invalid")
	}
	return dataset, nil
}

func (s *Store) List(ctx context.Context, task string) ([]Dataset, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT metadata FROM agent_datasets WHERE ($1='' OR task_id=$1) ORDER BY created_at DESC,id LIMIT 50`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	datasets := []Dataset{}
	for rows.Next() {
		var metadata []byte
		var dataset Dataset
		if err = rows.Scan(&metadata); err != nil {
			return nil, err
		}
		if err = graph.Decode(metadata, &dataset); err != nil {
			return nil, fmt.Errorf("stored dataset metadata is invalid")
		}
		datasets = append(datasets, dataset)
	}
	return datasets, rows.Err()
}

func (s *Store) Profile(ctx context.Context, id string) (Profile, error) {
	dataset, err := s.Get(ctx, id)
	return dataset.Profile, err
}

func (s *Store) Inputs(ctx context.Context, id, split string, withLabels bool) (graph.Artifact, error) {
	name, err := canonicalSplit(split)
	if err != nil {
		return graph.Artifact{}, err
	}
	dataset, err := s.Get(ctx, id)
	if err != nil {
		return graph.Artifact{}, err
	}
	ref := dataset.Splits[name].Input
	if withLabels {
		ref = dataset.Splits[name].Labeled
	}
	if _, err := s.Files.Get(ref); err != nil {
		return graph.Artifact{}, err
	}
	return ref, nil
}

func (s *Store) StageTraining(ctx context.Context, id, workspace string) error {
	dataset, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll("data", 0755); err != nil {
		return err
	}
	for _, name := range []string{"train", "development"} {
		if err = ctx.Err(); err != nil {
			return err
		}
		data, err := s.Files.Get(dataset.Splits[name].Labeled)
		if err != nil {
			return err
		}
		if err = stageFile(root, "data/"+name+".json", data); err != nil {
			return err
		}
	}
	profile, err := json.MarshalIndent(dataset.Profile, "", "  ")
	if err != nil {
		return err
	}
	return stageFile(root, "profile.json", profile)
}

// Publish only fully flushed files. Retries reuse identical bytes and never
// replace a candidate file, follow a symlink, or read a special file as data.
func stageFile(root *os.Root, name string, content []byte) error {
	temporary := path.Join(path.Dir(name), ".dataset-staging-"+rand.Text())
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return fmt.Errorf("cannot stage dataset input: %w", err)
	}
	defer root.Remove(temporary)
	if _, err = f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Link(temporary, name); os.IsExist(err) {
		existing, openErr := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if openErr != nil {
			return fmt.Errorf("cannot verify an existing staged input")
		}
		defer existing.Close()
		info, statErr := existing.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
			return fmt.Errorf("existing staged input differs from the frozen dataset")
		}
		data, readErr := io.ReadAll(io.LimitReader(existing, int64(len(content))+1))
		if readErr != nil || !bytes.Equal(data, content) {
			return fmt.Errorf("existing staged input differs from the frozen dataset")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot publish staged dataset input: %w", err)
	}
	directory, err := root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) InferenceInput(ctx context.Context, id string, input io.Reader) (graph.Artifact, int, error) {
	dataset, err := s.Get(ctx, id)
	if err != nil {
		return graph.Artifact{}, 0, err
	}
	raw, table, err := readCSV(ctx, input)
	if err != nil {
		return graph.Artifact{}, 0, err
	}
	byName := map[string]int{}
	for i, name := range table.headers {
		byName[strings.ToLower(name)] = i
	}
	indices := make([]int, len(dataset.FeatureColumns))
	for i, name := range dataset.FeatureColumns {
		index, exists := byName[strings.ToLower(name)]
		if !exists {
			return graph.Artifact{}, 0, fmt.Errorf("inference CSV is missing a selected feature")
		}
		indices[i] = index
	}
	data := Data{Rows: make([]map[string]any, 0, len(table.rows)), RowIDs: make([]string, 0, len(table.rows))}
	digest := graph.Hash(raw)
	for i, row := range table.rows {
		selected, err := selectedRow(row, indices, dataset.Profile.Fields)
		if err != nil {
			return graph.Artifact{}, 0, err
		}
		data.Rows = append(data.Rows, selected)
		data.RowIDs = append(data.RowIDs, rowID(digest, i))
	}
	ref, err := s.putJSON(data)
	return ref, len(data.Rows), err
}
