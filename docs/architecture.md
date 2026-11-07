# Runtime architecture

## Browser bridge

`afterthought serve` hosts the buildless frontend and `/api/` together on a loopback address, starting the same worker used by the CLI. The API does not add model-visible tools. A Postgres request admission record retains the normalized input digest before Temporal submission, so identical retries reuse the session identity. Pending requests remain visible before Setup has created a session row.

The browser polls the journal and Temporal execution status, renders bounded workspace files, and posts attributed feedback. Transcript DTOs exclude system/developer messages and opaque provider response items. File previews use a session-scoped root, reject special files and traversal, and return plain JSON. Mutations require JSON and reject foreign origins; provider credentials remain server-side.

The illustrative claims replay does not seed the live node library. Plain authoring sessions receive attributed task memory in fresh workspaces. CSV bootstrap sessions also receive permitted training/development data; node-tuning sessions receive immutable incumbent sources and development evidence.

## Executable CSV graphs

Dataset intake freezes operator-declared binary labels, selected features, inferred types, evaluation policy, and stratified train/development/audit membership. Audit rows stay outside authoring workspaces; serving inputs omit labels and targets. The 64 MiB artifact budget is separate from small Temporal envelopes. See `internal/dataset`.

`pipeline.BuildWorkflow` invokes the four-tool author to create v2 `candidate.json` with explicit fit/serve DAGs and self-contained Python `run(inputs, config)` modules. The supervisor snapshots source, wrapper, runtime image, config, bindings, and limits into immutable node revisions. It executes fit only on training data, then serves frozen held-out inputs. The trusted evaluator validates every prediction and computes metrics against retained labels; the author cannot self-report passing metrics. Runtime failures retain a packaged candidate for inspection and tuning.

`execution.GraphWorkflow` is a fixed deterministic topological interpreter with one Activity per node batch. A node receives read-only input/package mounts, no network or provider credentials, bounded resources/time, and an 80 MiB output tmpfs. The pinned wrapper implements the artifact request/result protocol; a bounded framed stdout transport carries output to the supervisor for version/identity/JSON/size/checksum verification. Failed nodes stop descendants. Atomic committed-winner records support recovery after output publication but before acknowledgement.

Tuning pins a parent release and selected `fit:ID` or `serve:ID`. The supervisor rejects edits outside that node's source/config, preserving every other revision and the graph topology. Serving edits reuse the fitted artifact; fit edits retrain. The whole graph is reevaluated, and development F1 regressions fail eligibility. Only development evidence enters improvement memory. Internal child/session IDs use an API-reserved namespace, and author sessions bind their full input digest.

## Human-only activation

Passing gates creates an eligible candidate, never an active release. A human supplies reviewer identity, explicit approval, a host-only random key, and the expected-current release pointer. The private key is atomically stored outside all sandbox mounts; invalid or partial keys fail closed. Approval is absent from agent tools and Workflow/Activity registration. A transaction checks gates and pointer, records approval, and selects the release. Every revision needs a separate human decision.

Batch admission requires an approved immutable release. Its graph/model are pinned for the execution; later selection changes affect new runs. Serving does not invoke the author or Hindsight, and every output row is validated before preview/download. Synthetic tests demonstrate mechanics, not real fraud accuracy. Audit labels are hidden from the agent, but repeated operator review of audit metrics consumes independence; use fresh final evidence and entity/time splits where appropriate.

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

Provider credentials stay in the worker and memory services. Memory extraction/reflection uses inference independently of authoring and is excluded from approved graph serving.

## Skills and next stages

Only skill name/description/path metadata is present initially. The agent uses `read` for full guidance. Skills describe methods and contracts; they do not supply initial business code. This mirrors [Pi's implementation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/system-prompt.ts) without embedding Pi's TypeScript runtime.

The current milestone supports local binary CSV intake, packaging, fit/serve execution, measured evaluations, human approval, batch inference, and selected-node revisions. Further work includes autonomous improvement triggers, richer task/schema types, final-audit lifecycle management, distributed storage/images, authentication, and production deployment controls.
