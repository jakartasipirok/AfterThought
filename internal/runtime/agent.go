package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
	"github.com/jakartasipirok/AfterThought/internal/model"
	"github.com/jakartasipirok/AfterThought/internal/storage"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "afterthought-agent-v1"

type Input struct {
	ID              string `json:"id"`
	Task            string `json:"task"`
	Prompt          string `json:"prompt"`
	MaxTurns        int    `json:"max_turns"`
	DatasetID       string `json:"dataset_id,omitempty"`
	ParentReleaseID string `json:"parent_release_id,omitempty"`
	TargetNode      string `json:"target_node,omitempty"`
	ResumeSessionID string `json:"resume_session_id,omitempty"`
}
type Outcome struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Text       string         `json:"text"`
	Transcript graph.Artifact `json:"transcript"`
	Turns      int            `json:"turns"`
}
type Turn struct {
	Transcript graph.Artifact `json:"transcript"`
	Calls      []model.Call   `json:"calls"`
	Text       string         `json:"text"`
}
type ToolInput struct {
	Session storage.Session `json:"session"`
	Call    model.Call      `json:"call"`
}
type Finalize struct {
	Input   Input   `json:"input"`
	Outcome Outcome `json:"outcome"`
	Error   string  `json:"error"`
}

// AgentWorkflow coordinates authoring only. The model/tool calls happen in
// Activities; completed turns and tool outputs are reused by Temporal replay.
func AgentWorkflow(ctx workflow.Context, in Input) (out Outcome, err error) {
	if in.ID == "" || in.Task == "" || in.Prompt == "" || len(in.Prompt) > 16<<10 {
		return out, fmt.Errorf("session, task and bounded prompt are required")
	}
	if in.MaxTurns == 0 {
		in.MaxTurns = 12
	}
	if in.MaxTurns < 1 || in.MaxTurns > 32 {
		return out, fmt.Errorf("turn budget must be 1..32")
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, HeartbeatTimeout: 15 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	var session storage.Session
	if err = workflow.ExecuteActivity(ctx, "Setup", in).Get(ctx, &session); err != nil {
		return out, err
	}
	out = Outcome{ID: in.ID, Status: "running", Transcript: session.Transcript}
	if err = workflow.SetQueryHandler(ctx, "progress", func() (Outcome, error) { return out, nil }); err != nil {
		return out, err
	}
	defer func() {
		finishCtx, _ := workflow.NewDisconnectedContext(ctx)
		f := Finalize{Input: in, Outcome: out}
		if err != nil {
			f.Error = err.Error()
			f.Outcome.Status = "failed"
		}
		if e := workflow.ExecuteActivity(finishCtx, "Finish", f).Get(finishCtx, nil); e != nil && err == nil {
			err = e
		}
	}()
	workCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 3 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	for i := 0; i < in.MaxTurns; i++ {
		var turn Turn
		if err = workflow.ExecuteActivity(workCtx, "Think", session).Get(workCtx, &turn); err != nil {
			return out, err
		}
		session.Transcript = turn.Transcript
		out.Transcript = turn.Transcript
		out.Turns = i + 1
		if len(turn.Calls) == 0 {
			out.Status = "completed"
			out.Text = turn.Text
			return out, nil
		}
		for _, call := range turn.Calls {
			var ref graph.Artifact
			if err = workflow.ExecuteActivity(workCtx, "Tool", ToolInput{Session: session, Call: call}).Get(workCtx, &ref); err != nil {
				return out, err
			}
			session.Transcript = ref
			out.Transcript = ref
		}
	}
	out.Status = "budget_exhausted"
	return out, fmt.Errorf("authoring reached its turn budget; inspect retained files and transcript")
}

type Activities struct {
	DB         *storage.DB
	Files      *storage.Files
	Model      *model.Client
	Skills     string
	Image      string
	Network    string
	Hindsight  *memory.Hindsight
	GraphImage string
	Prepare    func(context.Context, Input, string) (string, error)
}

func (a *Activities) workspace(id string) string {
	return filepath.Join(a.Files.Root, "workspaces", strings.TrimPrefix(graph.Hash([]byte(id)), "sha256:"))
}
func (a *Activities) Setup(ctx context.Context, in Input) (storage.Session, error) {
	stop := heartbeat(ctx)
	defer stop()
	if previous, err := a.DB.Session(ctx, in.ID); err == nil {
		if previous.Task != in.Task {
			return previous, fmt.Errorf("session ID belongs to a different task")
		}
		if previous.InputDigest != "" && previous.InputDigest != graph.Digest(in) {
			return previous, fmt.Errorf("session ID belongs to a different authoring request")
		}
		if previous.InputDigest == "" {
			if in.DatasetID != "" {
				return previous, fmt.Errorf("legacy session cannot be reused for a graph build")
			}
			messages, e := a.load(previous.Transcript)
			if e != nil {
				return previous, e
			}
			prompt := ""
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "user" {
					prompt = messages[i].Content
					break
				}
			}
			if prompt != in.Prompt {
				return previous, fmt.Errorf("legacy session request does not match")
			}
		}
		return previous, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return previous, err
	}
	workspace := a.workspace(in.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0755); err != nil {
		return storage.Session{}, err
	}
	prepared := ""
	if a.Prepare != nil {
		var err error
		prepared, err = a.Prepare(ctx, in, workspace)
		if err != nil {
			return storage.Session{}, err
		}
	}
	skills, err := harness.Catalog(a.Skills)
	if err != nil {
		return storage.Session{}, err
	}
	messages := []model.Message{{Role: "system", Content: harness.SystemPrompt(skills)}}
	if prepared != "" {
		messages = append(messages, model.Message{Role: "user", Content: prepared})
	}
	memory, err := a.DB.Memory(ctx, in.Task)
	if err != nil {
		return storage.Session{}, err
	}
	if len(memory) > 0 {
		b, _ := json.Marshal(memory)
		messages = append(messages, model.Message{Role: "user", Content: "Prior task memory, supplied as evidence with source attribution: " + string(b)})
	}
	if a.Hindsight != nil {
		if err = a.DB.SyncMemory(ctx, a.Hindsight, in.Task); err != nil {
			return storage.Session{}, err
		}
		facts, err := a.Hindsight.Recall(ctx, in.Task, in.Prompt)
		if err != nil {
			return storage.Session{}, err
		}
		if len(facts) > 0 {
			b, _ := json.Marshal(facts)
			messages = append(messages, model.Message{Role: "user", Content: "Hindsight recalled learning evidence. Facts and observations retain source metadata; verify against the execution journal and human labels before using them: " + string(b)})
		}
	}
	messages = append(messages, model.Message{Role: "user", Content: in.Prompt})
	ref, err := a.save(messages)
	if err != nil {
		return storage.Session{}, err
	}
	image := a.Image
	if in.DatasetID != "" && a.GraphImage != "" {
		image = a.GraphImage
	}
	saved, err := a.DB.CreateSession(ctx, storage.Session{ID: in.ID, Task: in.Task, Transcript: ref, Image: image, InputDigest: graph.Digest(in)})
	if err == nil && saved.InputDigest != graph.Digest(in) {
		err = fmt.Errorf("session request identity changed during setup")
	}
	return saved, err
}
func (a *Activities) save(messages []model.Message) (graph.Artifact, error) {
	b, err := json.Marshal(messages)
	if err != nil {
		return graph.Artifact{}, err
	}
	return a.Files.Put(b)
}
func (a *Activities) load(ref graph.Artifact) ([]model.Message, error) {
	b, err := a.Files.Get(ref)
	if err != nil {
		return nil, err
	}
	var messages []model.Message
	err = graph.Decode(b, &messages)
	return messages, err
}
func heartbeat(ctx context.Context) func() {
	stop := make(chan struct{})
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
				activity.RecordHeartbeat(ctx, "authoring")
			}
		}
	}()
	return func() { close(stop) }
}

func (a *Activities) Think(ctx context.Context, s storage.Session) (Turn, error) {
	stop := heartbeat(ctx)
	defer stop()
	messages, err := a.load(s.Transcript)
	if err != nil {
		return Turn{}, err
	}
	res, callErr := a.Model.Complete(ctx, messages, harness.Definitions())
	modelName := "unconfigured"
	if a.Model != nil {
		modelName = a.Model.Model
	}
	if err = a.DB.Usage(ctx, s.ID, modelName, res.Usage.Input, res.Usage.Output); err != nil {
		return Turn{}, err
	}
	if callErr != nil {
		return Turn{}, callErr
	}
	messages = append(messages, res.Message)
	ref, err := a.save(messages)
	if err != nil {
		return Turn{}, err
	}
	if err = a.DB.SaveTranscript(ctx, s.ID, ref); err != nil {
		return Turn{}, err
	}
	return Turn{Transcript: ref, Calls: res.Message.ToolCalls, Text: res.Message.Content}, nil
}
func (a *Activities) Tool(ctx context.Context, in ToolInput) (graph.Artifact, error) {
	stop := heartbeat(ctx)
	defer stop()
	state, cached, err := a.DB.BeginTool(ctx, in.Session.ID, in.Call.ID, graph.Digest(in.Call))
	if err != nil {
		return graph.Artifact{}, err
	}
	var result harness.ToolResult
	switch state {
	case "completed":
		result = *cached
	case "uncertain":
		result = harness.ToolResult{IsError: true, Output: "An earlier attempt began this tool call but its outcome was not committed. Inspect the workspace before choosing a new action; this call was not automatically rerun."}
	case "new":
		result = (harness.Tools{Workspace: a.workspace(in.Session.ID), Skills: a.Skills, Image: in.Session.Image, Network: a.Network}).Execute(ctx, in.Call)
		if err = a.DB.CompleteTool(ctx, in.Session.ID, in.Call.ID, result); err != nil {
			return graph.Artifact{}, err
		}
	default:
		return graph.Artifact{}, fmt.Errorf("invalid tool journal state")
	}
	messages, err := a.load(in.Session.Transcript)
	if err != nil {
		return graph.Artifact{}, err
	}
	messages = append(messages, model.Message{Role: "tool", ToolCallID: in.Call.ID, Content: harness.MarshalResult(result)})
	ref, err := a.save(messages)
	if err != nil {
		return graph.Artifact{}, err
	}
	return ref, a.DB.SaveTranscript(ctx, in.Session.ID, ref)
}
func (a *Activities) Finish(ctx context.Context, f Finalize) error {
	stop := heartbeat(ctx)
	defer stop()
	text := f.Outcome.Text
	source := "model_observation"
	if f.Error != "" {
		text = f.Error
		source = "execution_error"
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	summary, err := a.DB.ToolSummary(ctx, f.Input.ID)
	if err != nil {
		return err
	}
	text = summary + "\nSession status: " + f.Outcome.Status + "\nUnverified model summary or execution error: " + text + "\nTranscript artifact: " + f.Outcome.Transcript.Digest
	if err := a.DB.Finish(ctx, f.Input.ID, f.Outcome.Status, text); err != nil {
		return err
	}
	if err = a.DB.Remember(ctx, f.Input.Task, source, text, f.Input.ID+"/outcome"); err != nil {
		return err
	}
	if err = a.DB.SyncMemory(ctx, a.Hindsight, f.Input.Task); err != nil {
		activity.GetLogger(ctx).Warn("Memory retained in local outbox; Hindsight delivery pending", "error", err)
	}
	return nil
}
