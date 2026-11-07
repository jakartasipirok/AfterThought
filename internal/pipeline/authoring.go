package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/execution"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	agentruntime "github.com/jakartasipirok/AfterThought/internal/runtime"
)

func (s *Service) Workspace(id string) string {
	return filepath.Join(s.Files.Root, "workspaces", strings.TrimPrefix(graph.Hash([]byte(id)), "sha256:"))
}

// PrepareAuthoring runs inside Setup Activity, never inside Workflow code.
// Reserved audit data and the host approval key are not staged here.
func (s *Service) PrepareAuthoring(ctx context.Context, in agentruntime.Input, workspace string) (string, error) {
	if in.PolicyReviewID != "" {
		return s.preparePolicyReview(ctx, in, workspace)
	}
	policyNote, err := s.stagePolicy(ctx, in.Task, in.PolicyID, workspace)
	if err != nil {
		return "", err
	}
	if in.DatasetID == "" {
		return policyNote, nil
	}
	d, err := s.Datasets.Get(ctx, in.DatasetID)
	if err != nil {
		return "", err
	}
	if d.Task != in.Task {
		return "", fmt.Errorf("dataset belongs to a different task")
	}
	if err = s.Datasets.StageTraining(ctx, in.DatasetID, workspace); err != nil {
		return "", err
	}
	if in.ResumeSessionID != "" {
		if err = s.copyAuthoring(s.Workspace(in.ResumeSessionID), workspace); err != nil {
			return "", err
		}
	} else if in.ParentReleaseID != "" {
		parent, err := s.Release(ctx, in.ParentReleaseID)
		if err != nil {
			return "", err
		}
		if parent.Task != in.Task || parent.DatasetID != in.DatasetID {
			return "", fmt.Errorf("tuning requires the parent's frozen dataset and task")
		}
		if err = s.ExportDraft(parent.Graph, workspace); err != nil {
			return "", err
		}
		b, _ := json.Marshal(parent.Development)
		if err = os.WriteFile(filepath.Join(workspace, "incumbent-development.json"), b, 0644); err != nil {
			return "", err
		}
	}
	if err = os.WriteFile(filepath.Join(workspace, "graph-authoring.md"), []byte(authoringContract), 0644); err != nil {
		return "", err
	}
	return policyNote + " Executable graph task: read graph-authoring.md for the v2 node interface and profile.json for dataset metadata before implementing. Training/development data are staged under data/. Audit data and the human approval key are outside this workspace.", nil
}

func (s *Service) copyAuthoring(source, destination string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if e.IsDir() {
			if name == "data" || name == "knowledge" || name == "skills" || strings.Contains(name, "__pycache__") || strings.Count(name, "/") > 5 {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(destination, name), 0755)
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("authoring snapshot contains a symlink")
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil
		}
		ext := filepath.Ext(name)
		if ext != ".py" && ext != ".json" && ext != ".md" && ext != ".txt" {
			return nil
		}
		b, err := fs.ReadFile(root.FS(), name)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, name), b, 0644)
	})
}

func (s *Service) ExportDraft(release execution.Release, workspace string) error {
	d := execution.Draft{Version: "v2", Name: release.Name}
	if err := os.MkdirAll(filepath.Join(workspace, "nodes"), 0755); err != nil {
		return err
	}
	for _, phase := range []string{"fit", "serve"} {
		g := release.Fit
		if phase == "serve" {
			g = release.Serve
		}
		draft := execution.DraftGraph{Output: g.Output, Nodes: []execution.DraftNode{}}
		for _, n := range g.Nodes {
			name := "nodes/" + phase + "-" + n.ID + ".py"
			source, err := s.Files.Get(n.Source)
			if err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(workspace, name), source, 0644); err != nil {
				return err
			}
			draft.Nodes = append(draft.Nodes, execution.DraftNode{ID: n.ID, Source: name, Needs: n.Needs, Inputs: n.Inputs, Config: n.Config, TimeoutSeconds: n.TimeoutSeconds, MaxAttempts: n.MaxAttempts})
		}
		if phase == "fit" {
			d.Fit = draft
		} else {
			d.Serve = draft
		}
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workspace, "candidate.json"), b, 0644)
}

const authoringContract = `Executable graph authoring contract (trusted harness interface, not business implementation):
Use read/bash/edit/write only. The domain library starts empty. Read the graph-workflow skill and profile.json. Do not print raw records or direct identifiers into the model transcript; use Bash for aggregate profiling.

Author candidate.json with this data-only schema:
{"version":"v2","name":"your-name","fit":{"nodes":[...],"output":"fit-output-id"},"serve":{"nodes":[...],"output":"serve-output-id"}}
Each node: {"id":"stable_lowercase_id","source":"nodes/your-node.py","needs":[],"inputs":{"port":"$input"},"config":{},"timeout_seconds":60,"max_attempts":1}.
Every source is a SELF-CONTAINED Python module exposing run(inputs, config) -> JSON-serializable value. It receives already-decoded JSON values by port name, NOT artifact envelopes or filesystem paths. The trusted wrapper handles artifacts, invocation IDs, checksums and output files. Do not implement your own file protocol or use /input/request.json directly. Use relative workspace paths for tool calls.

The FIT graph has at least one node and runs ONCE on training data. The SERVE graph must have at least two meaningful connected nodes (e.g. separately tunable transformation, scoring, routing functions); choose the actual decomposition from the data and objective. Each graph has at most16 nodes, declared needs, and explicit port bindings. Inputs may bind "$input", a declared predecessor ID, or (in serve only) "$fit", the frozen fitted output. The fit output is the only trained state available during serving, so include fitted preprocessing and model state it needs. Nodes cannot import other workspace modules. numpy/pandas/scikit-learn/joblib are available in the generic runtime, with networking disabled during execution. For serialization, JSON parameters or base64 joblib payloads made only from standard library/sklearn classes are supported; do not pickle custom node-module classes.

data/train.json and data/development.json have {"rows":[feature objects],"labels":[0 or1],"row_ids":[stable string IDs]}. CSV values have frozen inferred types: number, boolean, string, or null; inspect profile.json. Learn and freeze any additional preprocessing using training data only. A serving input has {"rows":[feature objects],"row_ids":[stable IDs]} WITHOUT labels or target. The harness holds audit data outside your workspace. Split membership and metric thresholds were frozen at dataset intake. No holdout fitting, label-derived features, or fabricated metrics. Make randomness deterministic.

The final serving node MUST return exactly {"predictions":[{"row_id":"matching input row ID","probability":0.0,"decision":"accept or escalate or reject","reasons":["nonempty grounded reason"]}]} with one prediction per input row, preserving row identity, finite probability in[0,1], at least one reason. Decisions are recommendations; do not invent fraud ground truth. The human-declared target semantics and positive-label mapping are in profile.json. The backend computes actual accuracy/precision/recall/F1/FPR against reserved labels; a successful Python process is not evidence of predictive quality.

Write meaningful local tests and run them, then ensure candidate.json and ALL referenced source files exist. Put tunable thresholds, feature selections, and model parameters into each node's config instead of burying them in orchestration code. The supervisor snapshots every source/config into an immutable node revision and executes the actual fit and serve DAGs for evaluation. You cannot approve or promote a candidate. Human-only approval is required even when all gates pass.

For node improvement, candidate.json plus every incumbent node source is already staged. Change ONLY the specified node's source and/or config. Keep name, output IDs, other node sources/configs, topology, bindings, limits, and node IDs unchanged. The harness rejects edits outside that node. Review incumbent-development.json and attributed task memory. Compare development evidence, not inferred business success. Do not claim the prior source library is empty when an incumbent was explicitly supplied for tuning.`
