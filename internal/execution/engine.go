package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

type Engine struct {
	Files *storage.Files
	Image string
	// afterCommit simulates a worker lost after durable commit but before ack.
	afterCommit func(Invocation) error
}

func sourceSnapshot(workspace, name string) ([]byte, error) {
	if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Ext(name) != ".py" {
		return nil, fmt.Errorf("node source must be a relative Python file")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return nil, fmt.Errorf("source path cannot traverse directories")
		}
	}
	name = path.Clean(name)
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot snapshot source inside workspace")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source must be a regular file")
	}
	if info.Size() < 1 || info.Size() > graph.MaxPayload {
		return nil, fmt.Errorf("source size must be 1..%d", graph.MaxPayload)
	}
	b, err := io.ReadAll(io.LimitReader(f, graph.MaxPayload+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > graph.MaxPayload {
		return nil, fmt.Errorf("source exceeds size limit")
	}
	return b, nil
}

func (e *Engine) Package(ctx context.Context, workspace string, draft Draft) (Release, error) {
	var release Release
	if e == nil || e.Files == nil || !graph.ValidDigest(e.Image) {
		return release, fmt.Errorf("artifact store and pinned runtime image are required")
	}
	if draft.Version != Version {
		return release, fmt.Errorf("draft version must be v2")
	}
	wrapper, err := e.Files.Put([]byte(pythonWrapper))
	if err != nil {
		return release, err
	}
	pack := func(dg DraftGraph) (Graph, error) {
		g := Graph{Output: dg.Output, Nodes: make([]Node, 0, len(dg.Nodes))}
		if len(dg.Nodes) > 16 {
			return g, fmt.Errorf("too many nodes")
		}
		for _, d := range dg.Nodes {
			if err := ctx.Err(); err != nil {
				return g, err
			}
			source, err := sourceSnapshot(workspace, d.Source)
			if err != nil {
				return g, fmt.Errorf("node %s: %w", d.ID, err)
			}
			ref, err := e.Files.Put(source)
			if err != nil {
				return g, err
			}
			if len(d.Config) == 0 {
				d.Config = json.RawMessage(`{}`)
			}
			// Normalize numeric representation before both identity and Python
			// execution, including after Postgres JSONB round trips.
			d.Config, err = canonicalJSON(d.Config)
			if err != nil {
				return g, fmt.Errorf("node %s has invalid JSON config", d.ID)
			}
			if d.TimeoutSeconds == 0 {
				d.TimeoutSeconds = 60
			}
			if d.MaxAttempts == 0 {
				d.MaxAttempts = 1
			}
			n := Node{ID: d.ID, Image: e.Image, Source: ref, Wrapper: wrapper, Needs: d.Needs, Inputs: d.Inputs, Config: d.Config, TimeoutSeconds: d.TimeoutSeconds, MaxAttempts: d.MaxAttempts}
			n.PackageDigest = packageDigest(n)
			n.Revision = NodeRevision(n)
			g.Nodes = append(g.Nodes, n)
		}
		return g, nil
	}
	release = Release{Version: Version, Name: draft.Name}
	if release.Fit, err = pack(draft.Fit); err != nil {
		return Release{}, err
	}
	if release.Serve, err = pack(draft.Serve); err != nil {
		return Release{}, err
	}
	release.ID = ReleaseDigest(release)
	if err = ValidateRelease(release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func bind(n Node, input, fit graph.Artifact, results map[string]graph.Artifact) (map[string]graph.Artifact, error) {
	bound := make(map[string]graph.Artifact, len(n.Inputs))
	for _, port := range inputKeys(n.Inputs) {
		source := n.Inputs[port]
		ref := results[source]
		if source == "$input" {
			ref = input
		}
		if source == "$fit" {
			ref = fit
		}
		if !validArtifact(ref, storage.MaxArtifactBytes) {
			return nil, fmt.Errorf("node %s is missing input %s", n.ID, port)
		}
		bound[port] = ref
	}
	return bound, nil
}

func (e *Engine) Run(ctx context.Context, executionID string, g Graph, input, fit graph.Artifact) (RunResult, error) {
	out := RunResult{GraphDigest: GraphDigest(g)}
	if err := validateGraph(g, 1, true); err != nil {
		return out, err
	}
	order, _ := g.Order()
	results := map[string]graph.Artifact{}
	for _, n := range order {
		inputs, err := bind(n, input, fit, results)
		if err != nil {
			return out, err
		}
		inv := Invocation{ExecutionID: executionID, GraphDigest: out.GraphDigest, Node: n, Inputs: inputs}
		var result NodeResult
		for attempt := int32(0); attempt < n.MaxAttempts; attempt++ {
			result, err = e.RunNode(ctx, inv)
			if err == nil || ctx.Err() != nil {
				break
			}
		}
		out.Nodes = append(out.Nodes, result)
		if err != nil {
			return out, err
		}
		results[n.ID] = result.Output
	}
	out.Output = results[g.Output]
	return out, nil
}

type boundedLogs struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

const wireMagic = "ATGRAPH2\n"
const maxWireBytes = storage.MaxArtifactBytes + graph.MaxPayload + 64

// Unlike a host output directory, this transport has a hard aggregate memory
// bound. Cancel immediately on overflow instead of buffering arbitrary source
// writes. No promoted bytes.Buffer.ReadFrom can bypass Write.
type boundedWire struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
	limit    int
}

func (b *boundedWire) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.limit - b.buffer.Len()
	if left > n {
		left = n
	}
	if left > 0 {
		b.buffer.Write(p[:left])
	}
	if left < n && !b.exceeded {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	return n, nil
}

func (b *boundedLogs) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := (16 << 10) - b.buffer.Len()
	if left > n {
		left = n
	}
	if left > 0 {
		b.buffer.Write(p[:left])
	}
	return n, nil
}

func (e *Engine) RunNode(ctx context.Context, in Invocation) (result NodeResult, err error) {
	start := time.Now()
	result = NodeResult{ID: in.Node.ID, InvocationID: in.ID(), Revision: in.Node.Revision, Status: "failed"}
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
		}
	}()
	if err = in.validate(); err != nil {
		return result, err
	}
	if e == nil || e.Files == nil {
		return result, fmt.Errorf("artifact store is required")
	}
	legacy := in.legacy()
	committed, lookupErr := e.Files.Committed(legacy)
	if lookupErr == nil {
		result.Output = committed.Output
		result.Cached = true
		result.Status = "completed"
		return result, nil
	}
	if !errors.Is(lookupErr, os.ErrNotExist) {
		return result, lookupErr
	}
	source, err := e.Files.Get(in.Node.Source)
	if err != nil {
		return result, err
	}
	wrapper, err := e.Files.Get(in.Node.Wrapper)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(wrapper, []byte(pythonWrapper)) {
		return result, fmt.Errorf("runtime wrapper checksum mismatch")
	}
	work, err := os.MkdirTemp(filepath.Join(e.Files.Root, "work"), "graph-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	inputDir, packageDir := filepath.Join(work, "input"), filepath.Join(work, "package")
	for _, dir := range []string{inputDir, packageDir} {
		if err = os.Mkdir(dir, 0700); err != nil {
			return result, err
		}
	}
	for _, port := range inputKeys(in.Inputs) {
		ref := in.Inputs[port]
		var data []byte
		data, err = e.Files.Get(ref)
		if err != nil {
			return result, err
		}
		if !json.Valid(data) {
			return result, fmt.Errorf("node input artifact must contain JSON")
		}
		if err = os.WriteFile(filepath.Join(inputDir, strings.TrimPrefix(ref.Digest, "sha256:")), data, 0400); err != nil {
			return result, err
		}
	}
	request := struct {
		Version      string                    `json:"version"`
		InvocationID string                    `json:"invocation_id"`
		Inputs       map[string]graph.Artifact `json:"inputs"`
		Config       json.RawMessage           `json:"config"`
	}{Version, in.ID(), in.Inputs, in.Node.Config}
	encoded, err := canonicalJSON(request)
	if err != nil {
		return result, err
	}
	for name, data := range map[string][]byte{filepath.Join(inputDir, "request.json"): encoded, filepath.Join(packageDir, "source.py"): source, filepath.Join(packageDir, "wrapper.py"): wrapper} {
		if err = os.WriteFile(name, data, 0400); err != nil {
			return result, err
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.Node.TimeoutSeconds)*time.Second)
	defer cancel()
	name := "afterthought-node-" + strings.ToLower(rand.Text())
	args := []string{"run", "--rm", "--name", name, "--label", "afterthought.graph_node=true", "--label", "afterthought.deadline=" + strconv.FormatInt(time.Now().Add(time.Duration(in.Node.TimeoutSeconds)*time.Second).Unix(), 10), "--network", "none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--cpus=1", "--memory=768m", "--pids-limit=128", "--tmpfs", "/tmp:rw,nosuid,size=128m", "--tmpfs", "/output:rw,nosuid,noexec,size=80m,mode=1777", "--log-driver", "none", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--env", "HOME=/tmp", "--env", "PYTHONDONTWRITEBYTECODE=1", "--env", "OPENBLAS_NUM_THREADS=1", "--env", "OMP_NUM_THREADS=1", "--env", "MKL_NUM_THREADS=1", "--mount", "type=bind,src=" + inputDir + ",dst=/input,readonly", "--mount", "type=bind,src=" + packageDir + ",dst=/package,readonly", "--workdir", "/tmp", in.Node.Image, "python3", "/package/wrapper.py"}
	cmd := exec.CommandContext(runCtx, "docker", args...)
	var logs boundedLogs
	wire := boundedWire{limit: maxWireBytes, cancel: cancel}
	cmd.Stdout = &wire
	cmd.Stderr = &logs
	defer removeContainer(name)
	err = cmd.Run()
	if wire.exceeded {
		return result, fmt.Errorf("node %s output transport exceeded its bounded frame limit", in.Node.ID)
	}
	if err != nil {
		if runCtx.Err() != nil {
			return result, fmt.Errorf("node %s timed out or was canceled: %w", in.Node.ID, runCtx.Err())
		}
		return result, fmt.Errorf("node %s container failed: %w", in.Node.ID, err)
	}
	output, err := readFrame(wire.buffer.Bytes(), in.ID())
	if err != nil {
		return result, err
	}
	ref, err := e.Files.Put(output)
	if err != nil {
		return result, err
	}
	committed, err = e.Files.Commit(legacy, ref)
	if err != nil {
		return result, err
	}
	result.Output = committed.Output
	result.Status = "completed"
	if e.afterCommit != nil {
		if err = e.afterCommit(in); err != nil {
			return result, err
		}
	}
	return result, nil
}

func readFrame(data []byte, id string) ([]byte, error) {
	if len(data) > maxWireBytes || !bytes.HasPrefix(data, []byte(wireMagic)) {
		return nil, fmt.Errorf("invalid node output frame")
	}
	data = data[len(wireMagic):]
	newline := bytes.IndexByte(data, '\n')
	if newline < 1 || newline > 8 {
		return nil, fmt.Errorf("invalid node output frame length")
	}
	headerLength, err := strconv.Atoi(string(data[:newline]))
	if err != nil || headerLength < 1 || headerLength > graph.MaxPayload {
		return nil, fmt.Errorf("invalid node output frame length")
	}
	data = data[newline+1:]
	if len(data) < headerLength {
		return nil, fmt.Errorf("truncated node output frame")
	}
	header, output := data[:headerLength], data[headerLength:]
	if err = validateResult(header, output, id); err != nil {
		return nil, err
	}
	return output, nil
}

func validateResult(header, output []byte, id string) error {
	var result struct {
		Version      string         `json:"version"`
		InvocationID string         `json:"invocation_id"`
		Output       graph.Artifact `json:"output"`
	}
	if err := graph.Decode(header, &result); err != nil || result.Version != Version || result.InvocationID != id || !validArtifact(result.Output, storage.MaxArtifactBytes) {
		return fmt.Errorf("invalid node result protocol")
	}
	if int64(len(output)) != result.Output.Size || graph.Hash(output) != result.Output.Digest || !json.Valid(output) {
		return fmt.Errorf("node output checksum or JSON mismatch")
	}
	return nil
}

func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("missing node output")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("node output must be a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("cannot read bounded node output")
	}
	return b, nil
}
func readResult(dir, id string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := readRegular(root, "result.json", graph.MaxPayload)
	if err != nil {
		return nil, err
	}
	output, err := readRegular(root, "output.json", storage.MaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	if err = validateResult(b, output, id); err != nil {
		return nil, err
	}
	return output, nil
}
func removeContainer(name string) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
}

// ReapExpired removes orphaned containers after a worker dies. No graph source
// can change these labels; only the supervisor talks to the Docker daemon.
func ReapExpired(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=afterthought.graph_node=true", "--format", `{{.ID}} {{.Label "afterthought.deadline"}}`)
	var output boundedLogs
	cmd.Stdout = &output
	if cmd.Run() != nil {
		return
	}
	for _, line := range strings.Split(output.buffer.String(), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		deadline, err := strconv.ParseInt(parts[1], 10, 64)
		if err == nil && deadline <= time.Now().Unix() {
			removeContainer(parts[0])
		}
	}
}
