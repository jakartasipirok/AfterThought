package graph

import (
	"encoding/json"
	"strings"
	"testing"
)

func validGraph() Manifest {
	return Manifest{Version: Version, Name: "test", Output: "second", Nodes: []Node{{ID: "first", Image: "sha256:" + strings.Repeat("1", 64), Needs: []string{}, Inputs: map[string]string{"data": "$input"}, Config: json.RawMessage(`{}`), TimeoutSeconds: 10, MaxAttempts: 1}, {ID: "second", Image: "sha256:" + strings.Repeat("2", 64), Needs: []string{"first"}, Inputs: map[string]string{"data": "first"}, Config: json.RawMessage(`{}`), TimeoutSeconds: 10, MaxAttempts: 1}}}
}
func TestGraphAdmission(t *testing.T) {
	g := validGraph()
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	g.Nodes[0].Needs = []string{"second"}
	if g.Validate() == nil {
		t.Fatal("cycle admitted")
	}
	g = validGraph()
	g.Nodes[1].Inputs["data"] = "missing"
	if g.Validate() == nil {
		t.Fatal("undeclared binding admitted")
	}
	g = validGraph()
	g.Nodes[0].Image = "mutable:latest"
	if g.Validate() == nil {
		t.Fatal("mutable package admitted")
	}
}
func TestCanonicalDigestAndNoInlineCommands(t *testing.T) {
	a := validGraph()
	b := validGraph()
	a.Nodes[0].Config = json.RawMessage(`{"a":1,"b":2}`)
	b.Nodes[0].Config = json.RawMessage(`{ "b":2, "a":1 }`)
	if Digest(a) != Digest(b) {
		t.Fatal("config key order changed graph identity")
	}
	if Decode([]byte(`{"version":"v1","command":"shell"}`), &Manifest{}) == nil {
		t.Fatal("inline host command accepted")
	}
}
