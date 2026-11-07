package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

// Generic arithmetic fixtures are authored only by tests. The shipped library
// remains empty and the runtime has no fraud or ML scoring implementation.
func draftFixture(t *testing.T, workspace string) Draft {
	t.Helper()
	sources := map[string]string{
		"fit.py":     "def run(inputs, config):\n    return {'offset': sum(inputs['data']['values'])}\n",
		"prepare.py": "def run(inputs, config):\n    return {'values': [v + inputs['fit']['offset'] for v in inputs['data']['values']]}\n",
		"score.py":   "def run(inputs, config):\n    return {'values': [v * config['factor'] for v in inputs['prepared']['values']]}\n",
	}
	for name, source := range sources {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return Draft{Version: Version, Name: "test-only-arithmetic", Fit: DraftGraph{Nodes: []DraftNode{{ID: "fit", Source: "fit.py", Inputs: map[string]string{"data": "$input"}}}, Output: "fit"}, Serve: DraftGraph{Nodes: []DraftNode{
		{ID: "prepare", Source: "prepare.py", Inputs: map[string]string{"data": "$input", "fit": "$fit"}},
		{ID: "score", Source: "score.py", Needs: []string{"prepare"}, Inputs: map[string]string{"prepared": "prepare"}, Config: json.RawMessage(`{"factor":2}`)},
	}, Output: "score"}}
}
func fixtureEngine(t *testing.T) (*Engine, string, Draft, Release) {
	t.Helper()
	workspace := t.TempDir()
	files, err := storage.NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Files: files, Image: graph.Hash([]byte("test-image"))}
	draft := draftFixture(t, workspace)
	release, err := e.Package(context.Background(), workspace, draft)
	if err != nil {
		t.Fatal(err)
	}
	return e, workspace, draft, release
}
func rehash(r *Release) {
	for _, g := range []*Graph{&r.Fit, &r.Serve} {
		for i := range g.Nodes {
			n := &g.Nodes[i]
			n.PackageDigest = packageDigest(*n)
			n.Revision = NodeRevision(*n)
		}
	}
	r.ID = ReleaseDigest(*r)
}
func clone(t *testing.T, r Release) Release {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out Release
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPackageSnapshotsAndCanonicalIdentity(t *testing.T) {
	e, workspace, draft, r := fixtureEngine(t)
	if r.ID == "" || ValidateRelease(r) != nil {
		t.Fatal("release is not self-consistent")
	}
	if r.Fit.Nodes[0].TimeoutSeconds != 60 || r.Fit.Nodes[0].MaxAttempts != 1 {
		t.Fatal("defaults missing")
	}
	before := r.Serve.Nodes[1].Revision
	changed := clone(t, r)
	changed.Serve.Nodes[1].Config = json.RawMessage("{\n\"factor\": 2\n}")
	rehash(&changed)
	if changed.ID != r.ID || changed.Serve.Nodes[1].Revision != before {
		t.Fatal("JSON formatting changed identity")
	}
	if err := os.WriteFile(filepath.Join(workspace, "score.py"), []byte("def run(inputs, config): return {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldSource, err := e.Files.Get(r.Serve.Nodes[1].Source)
	if err != nil || !bytes.Contains(oldSource, []byte("factor")) {
		t.Fatal("packaged source changed with workspace")
	}
	newRelease, err := e.Package(context.Background(), workspace, draft)
	if err != nil {
		t.Fatal(err)
	}
	if newRelease.ID == r.ID || newRelease.Serve.Nodes[1].Revision == before || newRelease.Serve.Nodes[0].Revision != r.Serve.Nodes[0].Revision {
		t.Fatal("node revisions failed to isolate the source change")
	}
	if err := ValidateTuning(r, newRelease, "serve:score"); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSnapshotRejectsEscapesAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.py")
	if err := os.WriteFile(outside, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.py")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.py"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.py"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.py"), bytes.Repeat([]byte{'x'}, graph.MaxPayload+1), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, name := range []string{"../outside.py", "x/../outside.py", outside, "escape.py", "fifo.py", "directory.py", "large.py", "missing.py", "a\\b.py", "a.json"} {
		if _, err := sourceSnapshot(root, name); err == nil {
			t.Fatalf("accepted unsafe source %q", name)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("special-file open blocked")
	}
}

func TestReleaseRejectsInvalidGraphsAndCorruption(t *testing.T) {
	_, _, _, r := fixtureEngine(t)
	cases := map[string]func(*Release){
		"one serving node": func(c *Release) { c.Serve.Nodes = c.Serve.Nodes[:1]; c.Serve.Output = "prepare" },
		"cycle": func(c *Release) {
			c.Serve.Nodes[0].Needs = []string{"score"}
			c.Serve.Nodes[0].Inputs["loop"] = "score"
		},
		"dangling":           func(c *Release) { c.Serve.Nodes[1].Needs = []string{"missing"} },
		"unbound dependency": func(c *Release) { c.Serve.Nodes[1].Inputs["prepared"] = "$input" },
		"disconnected":       func(c *Release) { c.Serve.Nodes[1].Needs = nil; c.Serve.Nodes[1].Inputs["prepared"] = "$input" },
		"fit uses fit":       func(c *Release) { c.Fit.Nodes[0].Inputs["data"] = "$fit" },
		"invalid image":      func(c *Release) { c.Serve.Nodes[0].Image = "python:latest" },
		"invalid ref":        func(c *Release) { c.Serve.Nodes[0].Source.Size = -1 },
		"unknown wrapper":    func(c *Release) { c.Serve.Nodes[0].Wrapper.Digest = graph.Hash([]byte("evil")) },
		"too many attempts":  func(c *Release) { c.Serve.Nodes[0].MaxAttempts = 4 },
		"config scalar":      func(c *Release) { c.Serve.Nodes[0].Config = json.RawMessage(`[]`) },
		"bad port":           func(c *Release) { c.Serve.Nodes[0].Inputs["../../path"] = "$input" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := clone(t, r)
			change(&c)
			rehash(&c)
			if err := ValidateRelease(c); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
	c := clone(t, r)
	c.Serve.Nodes[0].Source.Digest = graph.Hash([]byte("corrupt"))
	c.ID = ReleaseDigest(c)
	if ValidateRelease(c) == nil {
		t.Fatal("revision mismatch ignored")
	}
	c = clone(t, r)
	c.ID = graph.Hash([]byte("wrong"))
	if ValidateRelease(c) == nil {
		t.Fatal("release ID mismatch ignored")
	}
	c = clone(t, r)
	c.Serve.Nodes[0].Config = json.RawMessage(`{`)
	if GraphDigest(c.Serve) != "" {
		t.Fatal("malformed JSON should not have identity")
	}
	if ValidateRelease(c) == nil {
		t.Fatal("malformed config accepted")
	}
}

func TestTuningProtectsEveryOtherField(t *testing.T) {
	_, _, _, r := fixtureEngine(t)
	valid := clone(t, r)
	valid.Serve.Nodes[1].Config = json.RawMessage(`{"factor":3}`)
	rehash(&valid)
	if err := ValidateTuning(r, valid, "serve:score"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Release){
		"other node": func(c *Release) { c.Serve.Nodes[0].Config = json.RawMessage(`{"change":true}`) },
		"runtime":    func(c *Release) { c.Serve.Nodes[1].Image = graph.Hash([]byte("different-image")) },
		"timeout":    func(c *Release) { c.Serve.Nodes[1].TimeoutSeconds++ },
		"binding":    func(c *Release) { c.Serve.Nodes[1].Inputs["additional"] = "$input" },
		"name":       func(c *Release) { c.Name = "renamed" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := clone(t, valid)
			change(&c)
			rehash(&c)
			if ValidateTuning(r, c, "serve:score") == nil {
				t.Fatal("protected change accepted")
			}
		})
	}
	for _, selector := range []string{"score", "serve:missing", "fit:score", "serve:score:extra"} {
		if ValidateTuning(r, valid, selector) == nil {
			t.Fatalf("invalid selector %s accepted", selector)
		}
	}
}

func TestJSONBNumericFormattingPreservesRevisionAndCommitIdentity(t *testing.T) {
	e, _, _, r := fixtureEngine(t)
	r.Fit.Nodes[0].Config = json.RawMessage(`{"large":9007199254740993,"value":1e3,"zero":-0.0,"fraction":0.1250}`)
	rehash(&r)
	roundtrip := clone(t, r)
	roundtrip.Fit.Nodes[0].Config = json.RawMessage(`{"fraction":1.25e-1,"value":1000,"large":9007199254740993.0,"zero":0}`)
	if NodeRevision(roundtrip.Fit.Nodes[0]) != r.Fit.Nodes[0].Revision || ReleaseDigest(roundtrip) != r.ID || GraphDigest(roundtrip.Fit) != GraphDigest(r.Fit) {
		t.Fatal("equivalent numeric JSONB representations changed identity")
	}
	if err := ValidateRelease(roundtrip); err != nil {
		t.Fatal(err)
	}
	input, _ := e.Files.Put([]byte(`{}`))
	output, _ := e.Files.Put([]byte(`{"offset":1}`))
	inv := Invocation{ExecutionID: "jsonb-roundtrip", GraphDigest: GraphDigest(r.Fit), Node: r.Fit.Nodes[0], Inputs: map[string]graph.Artifact{"data": input}}
	if _, err := e.Files.Commit(inv.legacy(), output); err != nil {
		t.Fatal(err)
	}
	inv.Node = roundtrip.Fit.Nodes[0]
	if got, err := e.RunNode(context.Background(), inv); err != nil || !got.Cached || got.Output != output {
		t.Fatal("numeric formatting prevented committed result reuse", err)
	}
	changed := clone(t, roundtrip)
	changed.Fit.Nodes[0].Config = json.RawMessage(`{"large":9007199254740992,"value":1000,"zero":0,"fraction":0.125}`)
	rehash(&changed)
	if changed.ID == r.ID {
		t.Fatal("large integer precision was lost")
	}
	if metadataDigest(json.RawMessage(`1000`)) == metadataDigest(json.RawMessage(`"1e3"`)) {
		t.Fatal("number/string identity collision")
	}
}

func TestResultProtocolRejectsUntrustedOutputs(t *testing.T) {
	good := []byte(`{"values":[1,2]}`)
	id := graph.Hash([]byte("invocation"))
	write := func(t *testing.T, dir string, output []byte, invocation string, version string, ref graph.Artifact) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"version": version, "invocation_id": invocation, "output": ref})
		if err := os.WriteFile(filepath.Join(dir, "result.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "output.json"), output, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ref := graph.Artifact{Digest: graph.Hash(good), Size: int64(len(good))}
	dir := t.TempDir()
	write(t, dir, good, id, Version, ref)
	if got, err := readResult(dir, id); err != nil || !bytes.Equal(got, good) {
		t.Fatal("valid result rejected", err)
	}
	for _, scenario := range []string{"wrong ID", "wrong version", "checksum", "invalid JSON", "symlink", "FIFO"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, good, id, Version, ref)
			switch scenario {
			case "wrong ID":
				write(t, dir, good, graph.Hash([]byte("other")), Version, ref)
			case "wrong version":
				write(t, dir, good, id, "v1", ref)
			case "checksum":
				write(t, dir, []byte(`{}`), id, Version, ref)
			case "invalid JSON":
				b := []byte("bad")
				write(t, dir, b, id, Version, graph.Artifact{Digest: graph.Hash(b), Size: int64(len(b))})
			case "symlink":
				os.Remove(filepath.Join(dir, "output.json"))
				outside := filepath.Join(t.TempDir(), "file")
				os.WriteFile(outside, good, 0600)
				os.Symlink(outside, filepath.Join(dir, "output.json"))
			case "FIFO":
				os.Remove(filepath.Join(dir, "output.json"))
				syscall.Mkfifo(filepath.Join(dir, "output.json"), 0600)
			}
			if _, err := readResult(dir, id); err == nil {
				t.Fatal("bad output accepted")
			}
		})
	}
}

func TestCommittedResultAndInputCollisionWithoutContainer(t *testing.T) {
	e, _, _, r := fixtureEngine(t)
	input, _ := e.Files.Put([]byte(`{"values":[1]}`))
	output, _ := e.Files.Put([]byte(`{"offset":1}`))
	in := Invocation{ExecutionID: "cached", GraphDigest: GraphDigest(r.Fit), Node: r.Fit.Nodes[0], Inputs: map[string]graph.Artifact{"data": input}}
	if _, err := e.Files.Commit(in.legacy(), output); err != nil {
		t.Fatal(err)
	}
	// No Docker or image exists here: returning proves the commit is reused first.
	got, err := e.RunNode(context.Background(), in)
	if err != nil || !got.Cached || got.Output != output {
		t.Fatalf("commit not reused: %+v %v", got, err)
	}
	other, _ := e.Files.Put([]byte(`{"values":[2]}`))
	in.Inputs["data"] = other
	if _, err = e.RunNode(context.Background(), in); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatal("identity collision accepted", err)
	}
}

func TestLogWriterBoundsCopyWithoutReaderFromBypass(t *testing.T) {
	var logs boundedLogs
	data := strings.Repeat("x", 2<<20)
	if n, err := logs.Write([]byte(data)); err != nil || n != len(data) {
		t.Fatal("writer did not drain")
	}
	if logs.buffer.Len() != 16<<10 {
		t.Fatal("logs were not bounded")
	}
	var second boundedLogs
	if _, err := io.Copy(&second, struct{ io.Reader }{strings.NewReader(data)}); err != nil {
		t.Fatal(err)
	}
	if second.buffer.Len() != 16<<10 {
		t.Fatal("io.Copy bypassed bounded Write")
	}
	// boundedLogs must not embed bytes.Buffer and inherit its unbounded ReadFrom.
	if _, ok := any(&logs).(io.ReaderFrom); ok {
		t.Fatal("unexpected bypass")
	}
}

func TestBoundedFrameValidatesIdentityChecksumAndSize(t *testing.T) {
	id := graph.Hash([]byte("frame-test"))
	output := []byte(`{"values":[1,2]}`)
	header, _ := json.Marshal(map[string]any{"version": Version, "invocation_id": id, "output": graph.Artifact{Digest: graph.Hash(output), Size: int64(len(output))}})
	frame := append([]byte(wireMagic+fmt.Sprint(len(header))+"\n"), header...)
	frame = append(frame, output...)
	if got, err := readFrame(frame, id); err != nil || !bytes.Equal(got, output) {
		t.Fatal("valid frame rejected", err)
	}
	for name, data := range map[string][]byte{
		"no frame":               output,
		"truncated":              frame[:len(frame)-1],
		"trailing data":          append(bytes.Clone(frame), 'x'),
		"excessive header":       []byte(wireMagic + "99999999\n{}"),
		"negative header":        []byte(wireMagic + "-1\n{}"),
		"noninteger header":      []byte(wireMagic + "NaN\n{}"),
		"missing header newline": []byte(wireMagic + "10{}"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readFrame(data, id); err == nil {
				t.Fatal("bad transport accepted")
			}
		})
	}
	if _, err := readFrame(frame, graph.Hash([]byte("other"))); err == nil {
		t.Fatal("wrong invocation accepted")
	}
	canceled := 0
	wire := boundedWire{limit: 1024, cancel: func() { canceled++ }}
	if _, err := io.Copy(&wire, struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 4096))}); err != nil {
		t.Fatal(err)
	}
	if !wire.exceeded || wire.buffer.Len() != 1024 || canceled != 1 {
		t.Fatal("wire did not stop buffering and cancel on overflow")
	}
	if _, ok := any(&wire).(io.ReaderFrom); ok {
		t.Fatal("unbounded ReaderFrom bypass")
	}
}
