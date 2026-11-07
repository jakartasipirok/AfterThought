package execution

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestGraphWorkflowOrdersNodesAndStopsDescendants(t *testing.T) {
	_, _, _, release := fixtureEngine(t)
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			suite := testsuite.WorkflowTestSuite{}
			env := suite.NewTestWorkflowEnvironment()
			var calls []string
			input := graph.Artifact{Digest: graph.Hash([]byte("input")), Size: 5}
			fit := graph.Artifact{Digest: graph.Hash([]byte("fit")), Size: 3}
			prepared := graph.Artifact{Digest: graph.Hash([]byte("prepared")), Size: 8}
			output := graph.Artifact{Digest: graph.Hash([]byte("output")), Size: 6}
			env.RegisterActivityWithOptions(func(ctx context.Context, in Invocation) (NodeResult, error) {
				calls = append(calls, in.Node.ID)
				if in.Node.ID == "prepare" {
					if in.Inputs["data"] != input || in.Inputs["fit"] != fit {
						t.Error("input or fitted asset binding changed")
					}
					if fail {
						return NodeResult{}, fmt.Errorf("fixture failure")
					}
					return NodeResult{ID: in.Node.ID, Output: prepared, Status: "completed"}, nil
				}
				if in.Inputs["prepared"] != prepared {
					t.Error("dependency result was not bound")
				}
				return NodeResult{ID: in.Node.ID, Output: output, Status: "completed"}, nil
			}, activity.RegisterOptions{Name: "GraphRunNode"})
			env.ExecuteWorkflow(GraphWorkflow, RunInput{ExecutionID: "workflow-test", Graph: release.Serve, Input: input, Fit: fit})
			if fail {
				if env.GetWorkflowError() == nil || !reflect.DeepEqual(calls, []string{"prepare"}) {
					t.Fatal("failed parent did not stop descendants", calls)
				}
				return
			}
			if env.GetWorkflowError() != nil {
				t.Fatal(env.GetWorkflowError())
			}
			var out RunResult
			if err := env.GetWorkflowResult(&out); err != nil {
				t.Fatal(err)
			}
			if out.Output != output || len(out.Nodes) != 2 || !reflect.DeepEqual(calls, []string{"prepare", "score"}) {
				t.Fatal("graph execution order or result changed", out, calls)
			}
		})
	}
}
