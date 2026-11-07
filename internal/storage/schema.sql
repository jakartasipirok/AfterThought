CREATE TABLE IF NOT EXISTS agent_sessions (
  id text PRIMARY KEY, task_id text NOT NULL, transcript jsonb NOT NULL,
  image text NOT NULL, status text NOT NULL, result text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS agent_tool_calls (
  session_id text NOT NULL REFERENCES agent_sessions(id), call_id text NOT NULL,
  request_digest text NOT NULL, status text NOT NULL, result jsonb,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(session_id,call_id)
);
CREATE TABLE IF NOT EXISTS agent_model_calls (
  id bigserial PRIMARY KEY, session_id text NOT NULL REFERENCES agent_sessions(id),
  model text NOT NULL, input_tokens bigint NOT NULL, output_tokens bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS agent_memory (
  id bigserial PRIMARY KEY, task_id text NOT NULL, source text NOT NULL,
  content text NOT NULL, dedup_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE agent_memory ADD COLUMN IF NOT EXISTS hindsight_operation_id text;
CREATE TABLE IF NOT EXISTS agent_api_requests (
  id text PRIMARY KEY, task_id text NOT NULL, input_digest text NOT NULL, input_json jsonb NOT NULL,
  run_id text NOT NULL DEFAULT '', launch_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
