package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type RunInput struct {
	ExecutionID string         `json:"execution_id"`
	Graph       Graph          `json:"graph"`
	Input       graph.Artifact `json:"input"`
	Fit         graph.Artifact `json:"fit"`
}

// GraphWorkflow records references and node results only. Input rows, models,
// source, and outputs stay in the artifact store rather than workflow history.
func GraphWorkflow(ctx workflow.Context, in RunInput) (out RunResult, err error) {
	if err = validateGraph(in.Graph, 1, true); err != nil {
		return out, err
	}
	if in.ExecutionID == "" || len(in.ExecutionID) > 240 {
		return out, fmt.Errorf("execution ID is required")
	}
	out.GraphDigest = GraphDigest(in.Graph)
	if err = workflow.SetQueryHandler(ctx, "progress", func() (RunResult, error) { return out, nil }); err != nil {
		return out, err
	}
	order, _ := in.Graph.Order()
	results := map[string]graph.Artifact{}
	for _, node := range order {
		inputs, bindErr := bind(node, in.Input, in.Fit, results)
		if bindErr != nil {
			return out, bindErr
		}
		invocation := Invocation{ExecutionID: in.ExecutionID, GraphDigest: out.GraphDigest, Node: node, Inputs: inputs}
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Duration(node.TimeoutSeconds+20) * time.Second,
			HeartbeatTimeout:    10 * time.Second, WaitForCancellation: true,
			RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: node.MaxAttempts},
		})
		var result NodeResult
		if err = workflow.ExecuteActivity(activityCtx, "GraphRunNode", invocation).Get(activityCtx, &result); err != nil {
			out.Nodes = append(out.Nodes, NodeResult{ID: node.ID, InvocationID: invocation.ID(), Revision: node.Revision, Status: "failed", Error: "Node execution failed; inspect worker diagnostics"})
			return out, err
		}
		out.Nodes = append(out.Nodes, result)
		results[node.ID] = result.Output
	}
	out.Output = results[in.Graph.Output]
	return out, nil
}

type Activities struct{ Engine *Engine }

func (a *Activities) GraphRunNode(ctx context.Context, in Invocation) (NodeResult, error) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, in.Node.ID)
			}
		}
	}()
	return a.Engine.RunNode(ctx, in)
}
