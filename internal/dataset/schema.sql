CREATE TABLE IF NOT EXISTS agent_datasets (
  id text PRIMARY KEY,
  task_id text NOT NULL,
  metadata jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_datasets_task_created_idx ON agent_datasets(task_id,created_at DESC,id);
