package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
)

var ErrAdmissionConflict = errors.New("session ID already belongs to a different request")

type WebSession struct {
	ID          string         `json:"id"`
	Task        string         `json:"task"`
	Status      string         `json:"status"`
	Result      string         `json:"result"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Transcript  graph.Artifact `json:"-"`
	LaunchError string         `json:"launch_error,omitempty"`
}

// AdmitWebSession persists identity before contacting Temporal. Retries never
// silently create a second billable execution or change the existing request.
func (d *DB) AdmitWebSession(ctx context.Context, id, task, digest string, input any) (bool, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO agent_api_requests(id,task_id,input_digest,input_json) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, task, digest, encode(input))
	if err != nil {
		return false, err
	}
	created := tag.RowsAffected() == 1
	if created {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE id=$1)`, id).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, ErrAdmissionConflict
		}
	} else {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT input_digest FROM agent_api_requests WHERE id=$1`, id).Scan(&existing); err != nil {
			return false, err
		}
		if existing != digest {
			return false, ErrAdmissionConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return created, nil
}
func (d *DB) WebLaunch(ctx context.Context, id, runID, message string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE agent_api_requests SET run_id=CASE WHEN $2<>'' THEN $2 ELSE run_id END,launch_error=$3,updated_at=now() WHERE id=$1`, id, runID, message)
	return err
}
func (d *DB) RejectWebAdmission(ctx context.Context, id, digest string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM agent_api_requests WHERE id=$1 AND input_digest=$2 AND run_id=''`, id, digest)
	return err
}
func (d *DB) WebMemoryAccepted(ctx context.Context, key string) (bool, error) {
	var accepted bool
	err := d.Pool.QueryRow(ctx, `SELECT hindsight_operation_id IS NOT NULL FROM agent_memory WHERE dedup_key=$1`, key).Scan(&accepted)
	return accepted, err
}

const webSessionSelect = `SELECT COALESCE(s.id,r.id),COALESCE(s.task_id,r.task_id),COALESCE(s.status,'queued'),COALESCE(s.result,''),COALESCE(s.created_at,r.created_at),GREATEST(s.updated_at,r.updated_at),s.transcript,COALESCE(r.launch_error,'') FROM agent_sessions s FULL JOIN agent_api_requests r ON r.id=s.id`

type webScanner interface{ Scan(...any) error }

func scanWebSession(row webScanner) (WebSession, error) {
	var s WebSession
	var b []byte
	err := row.Scan(&s.ID, &s.Task, &s.Status, &s.Result, &s.CreatedAt, &s.UpdatedAt, &b, &s.LaunchError)
	if err != nil {
		return s, err
	}
	if len(b) > 0 {
		err = graph.Decode(b, &s.Transcript)
	}
	return s, err
}
func (d *DB) WebSession(ctx context.Context, id string) (WebSession, error) {
	return scanWebSession(d.Pool.QueryRow(ctx, webSessionSelect+` WHERE COALESCE(s.id,r.id)=$1`, id))
}
func (d *DB) WebSessions(ctx context.Context, limit, offset int) ([]WebSession, error) {
	rows, err := d.Pool.Query(ctx, webSessionSelect+` ORDER BY COALESCE(s.created_at,r.created_at) DESC,COALESCE(s.id,r.id) DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebSession{}
	for rows.Next() {
		s, err := scanWebSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type WebTool struct {
	CallID    string              `json:"call_id"`
	Name      string              `json:"name"`
	Status    string              `json:"status"`
	Result    *harness.ToolResult `json:"result"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}
type WebUsage struct {
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CreatedAt    time.Time `json:"created_at"`
}

func (d *DB) WebJournal(ctx context.Context, id string) ([]WebTool, []WebUsage, error) {
	rows, err := d.Pool.Query(ctx, `SELECT call_id,status,result,created_at,updated_at FROM agent_tool_calls WHERE session_id=$1 ORDER BY created_at,call_id LIMIT 256`, id)
	if err != nil {
		return nil, nil, err
	}
	calls := []WebTool{}
	for rows.Next() {
		var call WebTool
		var b []byte
		if err = rows.Scan(&call.CallID, &call.Status, &b, &call.CreatedAt, &call.UpdatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if len(b) > 0 {
			var result harness.ToolResult
			if err = json.Unmarshal(b, &result); err != nil {
				rows.Close()
				return nil, nil, err
			}
			call.Result = &result
		}
		calls = append(calls, call)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	usageRows, err := d.Pool.Query(ctx, `SELECT model,input_tokens,output_tokens,created_at FROM agent_model_calls WHERE session_id=$1 ORDER BY id LIMIT 64`, id)
	if err != nil {
		return nil, nil, err
	}
	defer usageRows.Close()
	usage := []WebUsage{}
	for usageRows.Next() {
		var row WebUsage
		if err = usageRows.Scan(&row.Model, &row.InputTokens, &row.OutputTokens, &row.CreatedAt); err != nil {
			return nil, nil, err
		}
		usage = append(usage, row)
	}
	return calls, usage, usageRows.Err()
}
