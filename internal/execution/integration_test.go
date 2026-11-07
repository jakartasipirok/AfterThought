package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func integrationEngine(t *testing.T) (*Engine, string, Draft, Release) {
	t.Helper()
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires local Docker and Python runtime")
	}
	if err := os.MkdirAll("../../.afterthought", 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("../../.afterthought", "execution-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	files, err := storage.NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(files.Root, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("AFTERTHOUGHT_ML_IMAGE")
	if image == "" {
		image = "afterthought-ml:v1"
	}
	id, err := harness.ResolveImage(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Files: files, Image: id}
	draft := draftFixture(t, workspace)
	release, err := e.Package(context.Background(), workspace, draft)
	if err != nil {
		t.Fatal(err)
	}
	return e, workspace, draft, release
}
func mustPut(t *testing.T, e *Engine, data []byte) graph.Artifact {
	t.Helper()
	ref, err := e.Files.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestIntegrationFitServeZeroModelCallsAndCommitRecovery(t *testing.T) {
	e, _, _, release := integrationEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "unexpected model call", http.StatusInternalServerError)
	}))
	defer provider.Close()
	t.Setenv("OPENAI_BASE_URL", provider.URL)
	t.Setenv("OPENAI_API_KEY", "fixture-no-real-key")
	train := mustPut(t, e, []byte(`{"values":[1,2,3]}`))
	batch := mustPut(t, e, []byte(`{"values":[4,5]}`))
	fit, err := e.Run(ctx, "fit-run", release.Fit, train, graph.Artifact{})
	if err != nil {
		t.Fatal(err)
	}
	// Running an existing release must not depend on today's configured image.
	e.Image = graph.Hash([]byte("new-runtime-not-installed"))
	serve, err := e.Run(ctx, "batch-run", release.Serve, batch, fit.Output)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Files.Get(serve.Output)
	if err != nil || string(got) != `{"values":[20,22]}` {
		t.Fatalf("wrong graph output %s %v", got, err)
	}
	if len(serve.Nodes) != 2 || serve.Nodes[0].Revision == "" || providerCalls.Load() != 0 {
		t.Fatal("node metadata or zero-token execution failed")
	}
	for _, n := range serve.Nodes {
		if n.Cached {
			t.Fatal("first run unexpectedly cached")
		}
	}
	repeated, err := e.Run(ctx, "batch-run", release.Serve, batch, fit.Output)
	if err != nil || repeated.Output != serve.Output {
		t.Fatal("repeat changed committed output", err)
	}
	for _, n := range repeated.Nodes {
		if !n.Cached {
			t.Fatal("committed node reran")
		}
	}
	var calls atomic.Int32
	e.afterCommit = func(Invocation) error {
		calls.Add(1)
		return fmt.Errorf("worker died before completion acknowledgement")
	}
	inv := Invocation{ExecutionID: "lost-completion", GraphDigest: GraphDigest(release.Fit), Node: release.Fit.Nodes[0], Inputs: map[string]graph.Artifact{"data": train}}
	if _, err = e.RunNode(ctx, inv); err == nil {
		t.Fatal("crash seam did not run")
	}
	e.afterCommit = nil
	recovered, err := e.RunNode(ctx, inv)
	if err != nil || !recovered.Cached || recovered.Output != fit.Output || calls.Load() != 1 {
		t.Fatal("commit-before-ack did not recover", recovered, err)
	}
	if providerCalls.Load() != 0 {
		t.Fatal("batch execution called provider")
	}
}

func TestIntegrationNodeFailuresTimeoutAndLargeArtifacts(t *testing.T) {
	e, workspace, draft, release := integrationEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	input := mustPut(t, e, []byte(`{"values":[1]}`))
	fit := mustPut(t, e, []byte(`{"offset":1}`))
	for _, scenario := range []string{"failure", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			source := "def run(inputs, config):\n    raise ValueError('fixture')\n"
			if scenario == "timeout" {
				source = "import time\ndef run(inputs, config):\n    time.sleep(30)\n    return {}\n"
			}
			if err := os.WriteFile(filepath.Join(workspace, "prepare.py"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			draft.Serve.Nodes[0].TimeoutSeconds = 1
			r, err := e.Package(ctx, workspace, draft)
			if err != nil {
				t.Fatal(err)
			}
			out, err := e.Run(ctx, "failure-"+scenario, r.Serve, input, fit)
			if err == nil || len(out.Nodes) != 1 || out.Nodes[0].Status != "failed" {
				t.Fatal("failure did not stop descendants", out, err)
			}
			for _, n := range out.Nodes {
				if n.ID == "score" {
					t.Fatal("descendant ran after parent failure")
				}
			}
		})
	}
	// A payload greater than the protocol-envelope limit remains claim-checked.
	large, _ := json.Marshal(map[string]any{"values": []int{1}, "padding": strings.Repeat("x", 2<<20)})
	if len(large) <= graph.MaxPayload {
		t.Fatal("fixture not large enough")
	}
	ref := mustPut(t, e, large)
	out, err := e.Run(ctx, "large-input", release.Fit, ref, graph.Artifact{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Files.Get(out.Output)
	if err != nil || !bytes.Equal(got, []byte(`{"offset":1}`)) {
		t.Fatal("large artifact failed", err)
	}
	// The trusted wrapper and supervisor also permit large JSON outputs.
	if err = os.WriteFile(filepath.Join(workspace, "fit.py"), []byte("def run(inputs, config):\n    return inputs['data']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := e.Package(ctx, workspace, draft)
	if err != nil {
		t.Fatal(err)
	}
	largeOut, err := e.Run(ctx, "large-output", r.Fit, ref, graph.Artifact{})
	if err != nil {
		t.Fatal(err)
	}
	if largeOut.Output.Size <= graph.MaxPayload {
		t.Fatal("large output truncated")
	}
}

func TestIntegrationGraphWorkflowRecordedHistoryReplay(t *testing.T) {
	e, _, _, release := integrationEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	address := os.Getenv("TEMPORAL_ADDRESS")
	if address == "" {
		address = "127.0.0.1:7233"
	}
	c, err := client.Dial(client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := "graph-test-" + strings.ToLower(rand.Text())
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(GraphWorkflow)
	w.RegisterActivity(&Activities{Engine: e})
	if err = w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	input := mustPut(t, e, []byte(`{"values":[4,5]}`))
	fit := mustPut(t, e, []byte(`{"offset":6}`))
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue}, GraphWorkflow, RunInput{ExecutionID: queue, Graph: release.Serve, Input: input, Fit: fit})
	if err != nil {
		t.Fatal(err)
	}
	var result RunResult
	if err = run.Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	output, err := e.Files.Get(result.Output)
	if err != nil || string(output) != `{"values":[20,22]}` {
		t.Fatal("wrong workflow output", string(output), err)
	}
	history := &historypb.History{}
	iterator := c.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, event)
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(GraphWorkflow)
	if err = replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("recorded graph no longer replays: %v", err)
	}
}

func TestIntegrationContainerOutputStorageAndWireAreBounded(t *testing.T) {
	e, workspace, draft, _ := integrationEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	input := mustPut(t, e, []byte(`{"values":[1]}`))
	cases := []struct {
		name, source, want string
		fails              bool
	}{
		{"tmpfs aggregate cap", `import os
def run(inputs, config):
    size = os.statvfs('/output')
    limited = False
    try:
        with open('/output/extra', 'wb') as f:
            for i in range(100):
                f.write(b'x' * (1024 * 1024))
    except OSError as e:
        limited = e.errno == 28
    finally:
        os.unlink('/output/extra')
    return {'capacity': size.f_blocks * size.f_frsize, 'limited': limited}
`, `{"capacity":83886080,"limited":true}`, false},
		{"user print redirected", `def run(inputs, config):
    print('diagnostic' * 100000)
    return {'offset': 1}
`, `{"offset":1}`, false},
		{"native stdout overflow", `import os
def run(inputs, config):
    for i in range(128):
        os.write(1, b'x' * (1024 * 1024))
    return {}
`, "", true},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(workspace, "fit.py"), []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			draft.Fit.Nodes[0].TimeoutSeconds = 15
			r, err := e.Package(ctx, workspace, draft)
			if err != nil {
				t.Fatal(err)
			}
			out, err := e.Run(ctx, fmt.Sprintf("output-bound-%d", index), r.Fit, input, graph.Artifact{})
			if test.fails {
				if err == nil || !strings.Contains(err.Error(), "bounded frame limit") {
					t.Fatal("native stdout overflow not rejected", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.Files.Get(out.Output)
			if err != nil || string(got) != test.want {
				t.Fatal("unexpected bounded transport result", string(got), err)
			}
			entries, err := os.ReadDir(filepath.Join(e.Files.Root, "work"))
			if err != nil || len(entries) != 0 {
				t.Fatal("per-invocation host files were not cleaned", entries, err)
			}
		})
	}
}
