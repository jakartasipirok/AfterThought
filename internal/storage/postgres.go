package storage

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/memory"
)

//go:embed schema.sql
var schema string

type DB struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*DB, error) {
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("database unavailable")
	}
	return &DB{p}, nil
}
func (d *DB) Migrate(ctx context.Context) error { _, err := d.Pool.Exec(ctx, schema); return err }
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type Session struct {
	ID          string         `json:"id"`
	Task        string         `json:"task"`
	Transcript  graph.Artifact `json:"transcript"`
	Image       string         `json:"image"`
	Status      string         `json:"status"`
	InputDigest string         `json:"input_digest,omitempty"`
}

func (d *DB) CreateSession(ctx context.Context, s Session) (Session, error) {
	_, err := d.Pool.Exec(ctx, `INSERT INTO agent_sessions(id,task_id,transcript,image,status,input_digest) VALUES($1,$2,$3,$4,'running',$5) ON CONFLICT DO NOTHING`, s.ID, s.Task, encode(s.Transcript), s.Image, s.InputDigest)
	if err != nil {
		return s, err
	}
	return d.Session(ctx, s.ID)
}
func (d *DB) Session(ctx context.Context, id string) (Session, error) {
	var s Session
	var b []byte
	err := d.Pool.QueryRow(ctx, `SELECT id,task_id,transcript,image,status,input_digest FROM agent_sessions WHERE id=$1`, id).Scan(&s.ID, &s.Task, &b, &s.Image, &s.Status, &s.InputDigest)
	if err != nil {
		return s, err
	}
	if err = graph.Decode(b, &s.Transcript); err != nil {
		return s, err
	}
	return s, nil
}
func (d *DB) SaveTranscript(ctx context.Context, id string, ref graph.Artifact) error {
	_, err := d.Pool.Exec(ctx, `UPDATE agent_sessions SET transcript=$2,updated_at=now() WHERE id=$1`, id, encode(ref))
	return err
}
func (d *DB) Finish(ctx context.Context, id, status, message string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE agent_sessions SET status=$2,result=$3,updated_at=now() WHERE id=$1`, id, status, message)
	return err
}
func (d *DB) Usage(ctx context.Context, id, model string, in, out int64) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO agent_model_calls(session_id,model,input_tokens,output_tokens) VALUES($1,$2,$3,$4)`, id, model, in, out)
	return err
}
func (d *DB) Remember(ctx context.Context, task, source, content, key string) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO agent_memory(task_id,source,content,dedup_key) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, task, source, content, key)
	return err
}

// The raw event is the authoritative outbox. Hindsight contains learned projections.
func (d *DB) SyncMemory(ctx context.Context, h *memory.Hindsight, task string) error {
	if h == nil {
		return nil
	}
	rows, err := d.Pool.Query(ctx, `SELECT task_id,source,content,dedup_key FROM agent_memory WHERE task_id=$1 AND hindsight_operation_id IS NULL ORDER BY id LIMIT 20`, task)
	if err != nil {
		return err
	}
	events := []memory.Event{}
	for rows.Next() {
		var e memory.Event
		if err = rows.Scan(&e.Task, &e.Source, &e.Content, &e.Key); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		receipt, err := h.Retain(ctx, e)
		if err != nil {
			return err
		}
		if _, err = d.Pool.Exec(ctx, `UPDATE agent_memory SET hindsight_operation_id=$2 WHERE dedup_key=$1`, e.Key, receipt.OperationID); err != nil {
			return err
		}
	}
	return nil
}
func (d *DB) ToolSummary(ctx context.Context, id string) (string, error) {
	var completed, failed, incomplete int
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),count(*) FILTER(WHERE status='completed' AND result->>'is_error'='true'),count(*) FILTER(WHERE status<>'completed') FROM agent_tool_calls WHERE session_id=$1`, id).Scan(&completed, &failed, &incomplete)
	return fmt.Sprintf("Recorded tool calls: %d completed, %d returned errors, %d have unresolved outcomes. These are execution outcomes, not evidence of predictive accuracy.", completed, failed, incomplete), err
}

type Memory struct {
	Source  string `json:"source"`
	Content string `json:"content"`
}

func (d *DB) Memory(ctx context.Context, task string) ([]Memory, error) {
	rows, err := d.Pool.Query(ctx, `SELECT source,content FROM (SELECT id,source,content FROM agent_memory WHERE task_id=$1 ORDER BY id DESC LIMIT 20) recent ORDER BY id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		if err = rows.Scan(&m.Source, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// BeginTool journals intent before any side effect. An unresolved earlier attempt
// is returned to the model for inspection rather than blindly rerunning a shell.
func (d *DB) BeginTool(ctx context.Context, session, callID, request string) (string, *harness.ToolResult, error) {
	tag, err := d.Pool.Exec(ctx, `INSERT INTO agent_tool_calls(session_id,call_id,request_digest,status) VALUES($1,$2,$3,'running') ON CONFLICT DO NOTHING`, session, callID, request)
	if err != nil {
		return "", nil, err
	}
	if tag.RowsAffected() == 1 {
		return "new", nil, nil
	}
	var digest, status string
	var b []byte
	err = d.Pool.QueryRow(ctx, `SELECT request_digest,status,result FROM agent_tool_calls WHERE session_id=$1 AND call_id=$2`, session, callID).Scan(&digest, &status, &b)
	if err != nil {
		return "", nil, err
	}
	if digest != request {
		return "", nil, fmt.Errorf("tool call ID reused with different arguments")
	}
	if status != "completed" {
		return "uncertain", nil, nil
	}
	var result harness.ToolResult
	if err = graph.Decode(b, &result); err != nil {
		return "", nil, err
	}
	return "completed", &result, nil
}
func (d *DB) CompleteTool(ctx context.Context, session, callID string, result harness.ToolResult) error {
	_, err := d.Pool.Exec(ctx, `UPDATE agent_tool_calls SET status='completed',result=$3,updated_at=now() WHERE session_id=$1 AND call_id=$2`, session, callID, encode(result))
	return err
}
func (d *DB) Inspect(ctx context.Context, id string) (json.RawMessage, error) {
	var b []byte
	err := d.Pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'tools',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.created_at) FROM agent_tool_calls t WHERE t.session_id=s.id),'[]'::jsonb),'usage',COALESCE((SELECT jsonb_agg(to_jsonb(u) ORDER BY u.id) FROM agent_model_calls u WHERE u.session_id=s.id),'[]'::jsonb)) FROM agent_sessions s WHERE s.id=$1`, id).Scan(&b)
	return b, err
}
