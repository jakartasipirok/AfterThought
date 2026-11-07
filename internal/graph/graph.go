// Package graph contains the versioned, data-only execution contract.
package graph

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
)

const Version = "v1"
const MaxPayload = 1 << 20

type Artifact struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func Hash(data []byte) string { h := sha256.Sum256(data); return "sha256:" + hex.EncodeToString(h[:]) }

// Canonicalize object keys, including RawMessage configs, across JSONB round trips.
func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		panic(err)
	}
	b, err = json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return Hash(b)
}
func ValidDigest(s string) bool { return regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(s) }

func Decode(data []byte, out any) error {
	if len(data) > MaxPayload {
		return fmt.Errorf("JSON exceeds %d bytes", MaxPayload)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

type Node struct {
	ID             string            `json:"id"`
	Image          string            `json:"image"`
	Needs          []string          `json:"needs"`
	Inputs         map[string]string `json:"inputs"` // $input or a declared predecessor ID
	Config         json.RawMessage   `json:"config"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxAttempts    int32             `json:"max_attempts"`
}

type Manifest struct {
	Version string `json:"version"`
	Name    string `json:"name"`
	Nodes   []Node `json:"nodes"`
	Output  string `json:"output"`
}

func (g Manifest) Validate() error {
	encoded, err := json.Marshal(g)
	if err != nil || len(encoded) > 64<<10 {
		return fmt.Errorf("manifest exceeds the 64 KiB history budget")
	}
	if g.Version != Version || g.Name == "" || len(g.Nodes) == 0 || len(g.Nodes) > 16 {
		return fmt.Errorf("unsupported version, missing name, or graph size outside 1..16")
	}
	nodes := map[string]Node{}
	for _, n := range g.Nodes {
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`).MatchString(n.ID) {
			return fmt.Errorf("invalid node ID %q", n.ID)
		}
		if _, ok := nodes[n.ID]; ok {
			return fmt.Errorf("duplicate node %s", n.ID)
		}
		if !ValidDigest(n.Image) {
			return fmt.Errorf("node %s requires an immutable local image ID", n.ID)
		}
		if n.TimeoutSeconds < 1 || n.TimeoutSeconds > 300 || n.MaxAttempts < 1 || n.MaxAttempts > 3 {
			return fmt.Errorf("node %s has out-of-policy limits", n.ID)
		}
		if len(n.Inputs) == 0 || !json.Valid(n.Config) {
			return fmt.Errorf("node %s requires inputs and JSON config", n.ID)
		}
		nodes[n.ID] = n
	}
	if _, ok := nodes[g.Output]; !ok {
		return fmt.Errorf("missing graph output node")
	}
	for _, n := range g.Nodes {
		deps := map[string]bool{}
		for _, dep := range n.Needs {
			if _, ok := nodes[dep]; !ok || dep == n.ID || deps[dep] {
				return fmt.Errorf("invalid dependency %s -> %s", dep, n.ID)
			}
			deps[dep] = true
		}
		for _, source := range n.Inputs {
			if source != "$input" && !deps[source] {
				return fmt.Errorf("node %s binds undeclared dependency %s", n.ID, source)
			}
		}
	}
	_, err = g.Order()
	return err
}

// Order sorts each ready set. No map iteration drives Workflow commands.
func (g Manifest) Order() ([]Node, error) {
	done := map[string]bool{}
	result := make([]Node, 0, len(g.Nodes))
	for len(result) < len(g.Nodes) {
		var ready []Node
		for _, n := range g.Nodes {
			if done[n.ID] {
				continue
			}
			ok := true
			for _, dep := range n.Needs {
				if !done[dep] {
					ok = false
				}
			}
			if ok {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("graph contains a cycle")
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
		for _, n := range ready {
			result = append(result, n)
			done[n.ID] = true
		}
	}
	return result, nil
}

type Invocation struct {
	ExecutionID string              `json:"execution_id"`
	GraphDigest string              `json:"graph_digest"`
	Node        Node                `json:"node"`
	Inputs      map[string]Artifact `json:"inputs"`
}

func (v Invocation) ID() string { return Hash([]byte(v.ExecutionID + "/" + v.Node.ID)) }

type Request struct {
	Version      string              `json:"version"`
	InvocationID string              `json:"invocation_id"`
	Inputs       map[string]Artifact `json:"inputs"`
	Config       json.RawMessage     `json:"config"`
}
type Result struct {
	Version      string   `json:"version"`
	InvocationID string   `json:"invocation_id"`
	Output       Artifact `json:"output"`
}
type Commit struct {
	InvocationID  string   `json:"invocation_id"`
	RequestDigest string   `json:"request_digest"`
	Output        Artifact `json:"output"`
}
