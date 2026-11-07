# Runtime architecture

## Browser bridge

`afterthought serve` hosts the buildless frontend and `/api/` together on a loopback address, starting the same worker used by the CLI. The API does not add model-visible tools. A Postgres request admission record retains the normalized input digest before Temporal submission, so identical retries reuse the session identity. Pending requests remain visible before Setup has created a session row.

The browser polls the journal and Temporal execution status, renders bounded workspace files, and posts attributed feedback. Transcript DTOs exclude system/developer messages and opaque provider response items. File previews use a session-scoped root, reject special files and traversal, and return plain JSON. Mutations require JSON and reject foreign origins; provider credentials remain server-side.

The illustrative claims replay is separate from these live sessions. Its policy does not seed the authoring library or establish a promoted graph. Reusing a task scope carries learning evidence into a fresh workspace; generated code is not yet a shared, versioned node library.

## Durable authoring

`AgentWorkflow` is a bounded Go/Temporal loop. Setup persists an initial transcript with progressive skill metadata, attributed task memory, and the user request. Each Think Activity invokes the configured model and returns a retained transcript reference plus tool calls. Tool Activities run serially in the model's returned order. Completion or failure is journaled and retained as learning evidence.

No model transport, ordinary goroutine, shell, file I/O, or random/time access occurs in Workflow code. Temporal replay reuses recorded Activity results. Transcripts live in content-addressed local storage; Postgres holds session state, tool-call intent/results, provider-reported usage, and the memory outbox.

The four visible tools are `read`, `bash`, `edit`, and `write`. No `detect_fraud`, `query_postgres`, bespoke scoring function, or prebuilt graph is supplied. The agent authors candidates using generic operations. A Bash/Python runtime is an environment, not a domain-node library.

## Tool boundary

File tools use a workspace-scoped `os.Root`, reject absolute/traversing paths, and bound content. Exact edits must match once. Skill files use a separate read-only root. Bash runs in a disposable container with an immutable retained image ID, resource/time limits, and a bounded output buffer. It receives only workspace and skills mounts, without host credentials or a Docker socket. Network is disabled unless the operator enables it.

Tool intent is persisted before execution. A completed call ID returns its cached result; reuse with different arguments fails. If an earlier attempt began but its outcome is unknown, the harness returns that uncertainty instead of blindly repeating a shell command. The agent can inspect the workspace before choosing a new action. This is not an exactly-once side-effect guarantee. Model/tool Activities have one automatic attempt to avoid hidden duplicate spend or effects; completed Activities survive ordinary Workflow replay.

## Hindsight

The adapter targets the [v1.2.0 OpenAPI contract](https://github.com/vectorize-io/hindsight/blob/v1.2.0/hindsight-docs/static/openapi.json):

- `POST /v1/default/banks/{bank}/memories` for async retain;
- `POST /v1/default/banks/{bank}/memories/recall` for bounded retrieval;
- `POST /v1/default/banks/{bank}/reflect` for explicit reflection.

Task-derived bank IDs plus strict task tags scope retrieval. Raw event keys yield stable document IDs and operation UUIDs. Postgres acknowledges an outbox entry only after Hindsight accepts its operation. A lost response can be retried with the same operation identity. No initial facts or successful domain techniques are seeded.

Recent journal records give immediate feedback context; Hindsight adds learned semantic/temporal/graph retrieval. Recalled content retains source metadata and remains evidence to verify. Model observations, command failures, and human corrections are distinct sources. Hindsight does not establish whether a fraud label is true, guarantee model improvement, or replace evaluation.

Provider credentials stay in the worker and memory services. Memory extraction/reflection uses inference independently of authoring. The future serving path should execute approved artifacts without querying an LLM or Hindsight per case; new learning signals can be batched outside that path.

## Skills and next stages

Only skill name/description/path metadata is present initially. The agent uses `read` for full guidance. Skills describe methods and contracts; they do not supply initial business code. This mirrors [Pi's implementation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/system-prompt.ts) without embedding Pi's TypeScript runtime.

Next stages are generated-node packaging with isolated builds, retained shared artifacts, a generic graph executor, training/serving graph contracts, meaningful dataset evaluations, manual release gates, and reactive/proactive improvement triggers. The current code authors and tests candidate files; it does not claim the complete PRD or inference-free serving is implemented.
