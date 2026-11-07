package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

type Files struct{ Root string }

// Artifacts include CSV batches and fitted-model JSON. Keep this independent
// from the much smaller graph/protocol envelope decoding limit.
const MaxArtifactBytes = 64 << 20

func NewFiles(root string) (*Files, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	f := &Files{Root: root}
	for _, d := range []string{"objects", "commits", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			return nil, err
		}
	}
	return f, nil
}
func (f *Files) Path(ref graph.Artifact) (string, error) {
	if !graph.ValidDigest(ref.Digest) || ref.Size < 0 || ref.Size > MaxArtifactBytes {
		return "", fmt.Errorf("invalid artifact reference")
	}
	return filepath.Join(f.Root, "objects", strings.TrimPrefix(ref.Digest, "sha256:")), nil
}
func (f *Files) Put(data []byte) (graph.Artifact, error) {
	if len(data) > MaxArtifactBytes {
		return graph.Artifact{}, fmt.Errorf("artifact exceeds %d bytes", MaxArtifactBytes)
	}
	ref := graph.Artifact{Digest: graph.Hash(data), Size: int64(len(data))}
	path, err := f.Path(ref)
	if err != nil {
		return ref, err
	}
	if err = f.create(path, data); err != nil && !errors.Is(err, os.ErrExist) {
		return ref, err
	}
	_, err = f.Get(ref)
	return ref, err
}
func (f *Files) Get(ref graph.Artifact) ([]byte, error) {
	path, err := f.Path(ref)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != ref.Size {
		return nil, fmt.Errorf("artifact size or file type mismatch")
	}
	b, err := io.ReadAll(io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != ref.Size || graph.Hash(b) != ref.Digest {
		return nil, fmt.Errorf("artifact checksum mismatch")
	}
	return b, nil
}
func (f *Files) commitPath(id string) (string, error) {
	if !graph.ValidDigest(id) {
		return "", fmt.Errorf("invalid invocation ID")
	}
	return filepath.Join(f.Root, "commits", strings.TrimPrefix(id, "sha256:")+".json"), nil
}
func (f *Files) Committed(v graph.Invocation) (graph.Commit, error) {
	var c graph.Commit
	path, err := f.commitPath(v.ID())
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = graph.Decode(b, &c); err != nil {
		return c, err
	}
	if c.InvocationID != v.ID() || c.RequestDigest != graph.Digest(v) {
		return c, fmt.Errorf("invocation ID was reused with different inputs or release")
	}
	_, err = f.Get(c.Output)
	return c, err
}
func (f *Files) Commit(v graph.Invocation, output graph.Artifact) (graph.Commit, error) {
	if _, err := f.Get(output); err != nil {
		return graph.Commit{}, err
	}
	c := graph.Commit{InvocationID: v.ID(), RequestDigest: graph.Digest(v), Output: output}
	path, err := f.commitPath(v.ID())
	if err != nil {
		return c, err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if err = f.create(path, b); err != nil && !errors.Is(err, os.ErrExist) {
		return c, err
	}
	// A competing attempt may have won. Always return the committed winner.
	return f.Committed(v)
}

// Publish a fully flushed file with an atomic create-if-absent hard link.
// This backend requires one retained local filesystem; it is not object storage.
func (f *Files) create(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".staging-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Link(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
