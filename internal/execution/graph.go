// Package execution runs immutable, agent-authored Python graphs without an LLM.
// Domain functions are supplied by the authoring workspace, never by this package.
package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/storage"
)

const Version = "v2"

type Draft struct {
	Version string     `json:"version"`
	Name    string     `json:"name"`
	Fit     DraftGraph `json:"fit"`
	Serve   DraftGraph `json:"serve"`
}
type DraftGraph struct {
	Nodes  []DraftNode `json:"nodes"`
	Output string      `json:"output"`
}
type DraftNode struct {
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	Needs          []string          `json:"needs"`
	Inputs         map[string]string `json:"inputs"`
	Config         json.RawMessage   `json:"config"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxAttempts    int32             `json:"max_attempts"`
}
type Release struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Fit     Graph  `json:"fit"`
	Serve   Graph  `json:"serve"`
}
type Graph struct {
	Nodes  []Node `json:"nodes"`
	Output string `json:"output"`
}
type Node struct {
	ID             string            `json:"id"`
	Image          string            `json:"image"`
	Source         graph.Artifact    `json:"source"`
	Wrapper        graph.Artifact    `json:"wrapper"`
	PackageDigest  string            `json:"package_digest"`
	Revision       string            `json:"revision"`
	Needs          []string          `json:"needs"`
	Inputs         map[string]string `json:"inputs"`
	Config         json.RawMessage   `json:"config"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxAttempts    int32             `json:"max_attempts"`
}
type Invocation struct {
	ExecutionID string                    `json:"execution_id"`
	GraphDigest string                    `json:"graph_digest"`
	Node        Node                      `json:"node"`
	Inputs      map[string]graph.Artifact `json:"inputs"`
}
type RunResult struct {
	GraphDigest string         `json:"graph_digest"`
	Output      graph.Artifact `json:"output"`
	Nodes       []NodeResult   `json:"nodes"`
}
type NodeResult struct {
	ID           string         `json:"id"`
	InvocationID string         `json:"invocation_id"`
	Revision     string         `json:"revision"`
	Output       graph.Artifact `json:"output"`
	DurationMS   int64          `json:"duration_ms"`
	Cached       bool           `json:"cached"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// metadataDigest only receives data-only manifests; JSON object keys are
// canonicalized before hashing. The analyzer follows reflection/runtime internals
// of Go's JSON implementation, which do not affect this pure result.
//
//workflowcheck:ignore
func metadataDigest(v any) string {
	b, err := canonicalJSON(v)
	if err != nil {
		return ""
	}
	return graph.Hash(b)
}

// canonicalJSON is used both for metadata identity and the legacy commit
// request's nested config, so JSONB representation changes cannot bypass cache.
//
//workflowcheck:ignore
func canonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(canonicalNumbers(value))
}

// JSONB may render 1e3 as 1000. Normalize number lexemes without converting via
// float64, which would lose large integers and fitted model precision.
func canonicalNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = canonicalNumbers(item)
		}
	case []any:
		for i, item := range v {
			v[i] = canonicalNumbers(item)
		}
	case json.Number:
		s := string(v)
		sign := ""
		if strings.HasPrefix(s, "-") {
			sign = "-"
			s = s[1:]
		}
		exponent := new(big.Int)
		if pos := strings.IndexAny(s, "eE"); pos >= 0 {
			exponent.SetString(s[pos+1:], 10)
			s = s[:pos]
		}
		fractional := 0
		if pos := strings.IndexByte(s, '.'); pos >= 0 {
			fractional = len(s) - pos - 1
			s = s[:pos] + s[pos+1:]
		}
		s = strings.TrimLeft(s, "0")
		if s == "" {
			return json.Number("0")
		}
		digits := strings.TrimRight(s, "0")
		exponent.Add(exponent, big.NewInt(int64(len(s)-len(digits)-fractional)))
		// Plain decimals preserve integer-valued configuration parameters as
		// Python ints (sklearn's max_depth, n_estimators, etc.). Bound expansion
		// so hostile exponent lexemes cannot allocate unbounded memory.
		if exponent.IsInt64() {
			scale := exponent.Int64()
			if scale >= 0 && scale <= 8192-int64(len(digits)) {
				return json.Number(sign + digits + strings.Repeat("0", int(scale)))
			}
			if scale < 0 && scale >= -8192 {
				point := int64(len(digits)) + scale
				if point > 0 {
					return json.Number(sign + digits[:int(point)] + "." + digits[int(point):])
				}
				return json.Number(sign + "0." + strings.Repeat("0", int(-point)) + digits)
			}
		}
		return json.Number(sign + digits + "e" + exponent.String())
	}
	return value
}
func GraphDigest(g Graph) string     { return metadataDigest(g) }
func ReleaseDigest(r Release) string { r.ID = ""; return metadataDigest(r) }
func NodeRevision(n Node) string     { n.Revision = ""; return metadataDigest(n) }
func packageDigest(n Node) string {
	return metadataDigest(struct {
		Version string         `json:"version"`
		Image   string         `json:"image"`
		Source  graph.Artifact `json:"source"`
		Wrapper graph.Artifact `json:"wrapper"`
	}{Version, n.Image, n.Source, n.Wrapper})
}

func validArtifact(a graph.Artifact, max int64) bool {
	return graph.ValidDigest(a.Digest) && a.Size >= 0 && a.Size <= max
}
func validateNode(n Node) error {
	if !validID.MatchString(n.ID) || !graph.ValidDigest(n.Image) {
		return fmt.Errorf("node requires a valid ID and immutable image")
	}
	if !validArtifact(n.Source, graph.MaxPayload) || n.Source.Size == 0 || n.Wrapper != wrapperArtifact() {
		return fmt.Errorf("node %s has invalid source or untrusted wrapper", n.ID)
	}
	if n.TimeoutSeconds < 1 || n.TimeoutSeconds > 300 || n.MaxAttempts < 1 || n.MaxAttempts > 3 {
		return fmt.Errorf("node %s limits must be timeout 1..300 and attempts 1..3", n.ID)
	}
	var config map[string]json.RawMessage
	// Parsing a data-only JSON object is pure; no custom unmarshaler is used.
	//workflowcheck:ignore
	if len(n.Config) > 8<<10 || json.Unmarshal(n.Config, &config) != nil || config == nil {
		return fmt.Errorf("node %s config must be a bounded JSON object", n.ID)
	}
	if len(n.Inputs) == 0 || len(n.Inputs) > 18 {
		return fmt.Errorf("node %s needs 1..18 input ports", n.ID)
	}
	if n.PackageDigest != packageDigest(n) || n.Revision != NodeRevision(n) {
		return fmt.Errorf("node %s package or revision digest mismatch", n.ID)
	}
	return nil
}

func ValidateRelease(r Release) error {
	b, err := json.Marshal(r)
	if err != nil || len(b) > 128<<10 {
		return fmt.Errorf("release exceeds history metadata budget")
	}
	if r.Version != Version || strings.TrimSpace(r.Name) == "" || len(r.Name) > 120 {
		return fmt.Errorf("release needs version v2 and a bounded name")
	}
	if r.ID != ReleaseDigest(r) {
		return fmt.Errorf("release digest mismatch")
	}
	if err := validateGraph(r.Fit, 1, false); err != nil {
		return fmt.Errorf("fit: %w", err)
	}
	if err := validateGraph(r.Serve, 2, true); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func validateGraph(g Graph, minimum int, allowFit bool) error {
	if len(g.Nodes) < minimum || len(g.Nodes) > 16 {
		return fmt.Errorf("graph needs %d..16 nodes", minimum)
	}
	nodes := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		if err := validateNode(n); err != nil {
			return err
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("duplicate node %s", n.ID)
		}
		nodes[n.ID] = n
	}
	if _, exists := nodes[g.Output]; !exists {
		return fmt.Errorf("missing output node")
	}
	for _, n := range g.Nodes {
		deps := make(map[string]bool, len(n.Needs))
		for _, dep := range n.Needs {
			if _, exists := nodes[dep]; !exists || dep == n.ID || deps[dep] {
				return fmt.Errorf("invalid dependency %s -> %s", dep, n.ID)
			}
			deps[dep] = true
		}
		used := map[string]bool{}
		for _, port := range inputKeys(n.Inputs) {
			if !validID.MatchString(port) {
				return fmt.Errorf("invalid input port %q", port)
			}
			source := n.Inputs[port]
			if source == "$input" || (allowFit && source == "$fit") {
				continue
			}
			if !deps[source] {
				return fmt.Errorf("node %s binds undeclared source %s", n.ID, source)
			}
			used[source] = true
		}
		for _, dep := range n.Needs {
			if !used[dep] {
				return fmt.Errorf("dependency %s is not bound to an input", dep)
			}
		}
	}
	if _, err := g.Order(); err != nil {
		return err
	}
	ancestors := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if ancestors[id] {
			return
		}
		ancestors[id] = true
		for _, dep := range nodes[id].Needs {
			visit(dep)
		}
	}
	visit(g.Output)
	if len(ancestors) != len(nodes) {
		return fmt.Errorf("all nodes must contribute to the selected output")
	}
	return nil
}

// Order is stable across JSON round trips and Temporal replay.
func (g Graph) Order() ([]Node, error) {
	done := map[string]bool{}
	ordered := make([]Node, 0, len(g.Nodes))
	for len(ordered) < len(g.Nodes) {
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
			return nil, fmt.Errorf("graph contains a cycle or duplicate node ID")
		}
		slices.SortFunc(ready, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
		for _, n := range ready {
			ordered = append(ordered, n)
			done[n.ID] = true
		}
	}
	return ordered, nil
}
func inputKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	// Collection order is irrelevant; sorting below determines every consumer.
	//workflowcheck:ignore
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidateTuning allows exactly one node's source/config to change. Interfaces,
// dependencies, environment, limits, and all other nodes remain pinned.
func ValidateTuning(parent, candidate Release, selector string) error {
	if err := ValidateRelease(parent); err != nil {
		return err
	}
	if err := ValidateRelease(candidate); err != nil {
		return err
	}
	parts := strings.Split(selector, ":")
	if len(parts) != 2 || (parts[0] != "fit" && parts[0] != "serve") || !validID.MatchString(parts[1]) {
		return fmt.Errorf("node selector must be fit:id or serve:id")
	}
	if parent.Version != candidate.Version || parent.Name != candidate.Name {
		return fmt.Errorf("tuning cannot rename the release")
	}
	found := false
	for _, phase := range []string{"fit", "serve"} {
		pg, cg := parent.Fit, candidate.Fit
		if phase == "serve" {
			pg, cg = parent.Serve, candidate.Serve
		}
		if pg.Output != cg.Output || len(pg.Nodes) != len(cg.Nodes) {
			return fmt.Errorf("tuning cannot change graph structure")
		}
		porder, _ := pg.Order()
		corder, _ := cg.Order()
		for i, pn := range porder {
			cn := corder[i]
			if phase == parts[0] && pn.ID == parts[1] {
				found = true
				// Normalize only the allowed change and its derived identities.
				cn.Source, cn.Config, cn.PackageDigest, cn.Revision = pn.Source, pn.Config, pn.PackageDigest, pn.Revision
			}
			if metadataDigest(pn) != metadataDigest(cn) {
				return fmt.Errorf("tuning changed protected fields or another node")
			}
		}
	}
	if !found {
		return fmt.Errorf("selected node does not exist")
	}
	return nil
}

func (v Invocation) legacy() graph.Invocation {
	// Pure data-only JSON; no user-defined marshaler is present.
	//workflowcheck:ignore
	config, _ := canonicalJSON(struct {
		Revision string          `json:"revision"`
		Config   json.RawMessage `json:"config"`
	}{v.Node.Revision, v.Node.Config})
	return graph.Invocation{ExecutionID: v.ExecutionID + "/" + v.GraphDigest, GraphDigest: v.GraphDigest, Node: graph.Node{ID: v.Node.ID, Image: v.Node.Image, Needs: v.Node.Needs, Inputs: v.Node.Inputs, Config: config, TimeoutSeconds: v.Node.TimeoutSeconds, MaxAttempts: v.Node.MaxAttempts}, Inputs: v.Inputs}
}
func (v Invocation) ID() string { return v.legacy().ID() }
func (v Invocation) validate() error {
	if v.ExecutionID == "" || len(v.ExecutionID) > 240 || !graph.ValidDigest(v.GraphDigest) {
		return fmt.Errorf("execution identity and graph digest are required")
	}
	if err := validateNode(v.Node); err != nil {
		return err
	}
	if len(v.Inputs) != len(v.Node.Inputs) {
		return fmt.Errorf("input binding mismatch")
	}
	for _, port := range inputKeys(v.Node.Inputs) {
		if ref, ok := v.Inputs[port]; !ok || !validArtifact(ref, storage.MaxArtifactBytes) {
			return fmt.Errorf("invalid input artifact at %s", port)
		}
	}
	return nil
}
