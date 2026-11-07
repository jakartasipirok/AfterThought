package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/model"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestAgentWorkflowStopsOnBudgetAndDoesNotRetryTools(t *testing.T) {
	for _, toolFails := range []bool{false, true} {
		t.Run(fmt.Sprint(toolFails), func(t *testing.T) {
			suite := testsuite.WorkflowTestSuite{}
			env := suite.NewTestWorkflowEnvironment()
			ref := graph.Artifact{Digest: graph.Hash([]byte("transcript")), Size: 10}
			tools := 0
			finished := false
			env.RegisterActivityWithOptions(func(context.Context, Input) (storage.Session, error) {
				return storage.Session{ID: "s", Task: "t", Transcript: ref}, nil
			}, activity.RegisterOptions{Name: "Setup"})
			env.RegisterActivityWithOptions(func(context.Context, storage.Session) (Turn, error) {
				return Turn{Transcript: ref, Calls: []model.Call{{ID: "call", Type: "function", Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"true"}`}}}}, nil
			}, activity.RegisterOptions{Name: "Think"})
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (graph.Artifact, error) {
				tools++
				if toolFails {
					return ref, fmt.Errorf("tool outcome unknown")
				}
				return ref, nil
			}, activity.RegisterOptions{Name: "Tool"})
			env.RegisterActivityWithOptions(func(context.Context, Finalize) error { finished = true; return nil }, activity.RegisterOptions{Name: "Finish"})
			env.ExecuteWorkflow(AgentWorkflow, Input{ID: "s", Task: "t", Prompt: "test", MaxTurns: 1})
			if env.GetWorkflowError() == nil || tools != 1 || !finished {
				t.Fatalf("budget/failure behavior invalid: calls=%d finalized=%v", tools, finished)
			}
		})
	}
}
