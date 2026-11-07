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

## Cumulative learning and backtests

The repeated-learning controller requests an initial build followed by three revisions of one fit node. Each revision carries cumulative `feedback_dataset_ids`; the supervisor combines only compatible training/development partitions and retrains using the expanded training artifact. Original development/audit evaluation stays fixed. Validation rejects overlapping learning and reserved feature vectors. Evaluation-only datasets keep all rows in audit storage and cannot train a graph.

`backtest_dataset_ids` pin additional evaluation datasets. The supervisor executes the candidate against their reserved inputs and retains reports on the release. Benchmark labels and results stay outside authoring and memory. Candidate eligibility depends on original development/audit gates and development F1 against the parent. Completed rejected candidates remain available for further tuning.

In the prepared demo, the original 400 claims split into 240 training, 80 development, and 80 audit cases. The chart uses a separate balanced 560-case benchmark. **Original audit accuracy** checks the same original 80 cases after each revision. See [dataset notes](dataset-notes.md) for sampling and provenance.

## Automatic robustness review

Opted-in scopes review windows of 50 finished serving batches for the active release. The worker records aggregate signals and claims review windows under a task lock. Attributable failures can start a bounded, one-node repair workflow with frozen evaluation and a trusted replay of the triggering batch. Each candidate requires human approval. Review outcomes and approvals enter the attributed memory outbox. See the [Technical Design Document](technical-design.md) for cadence, limits, recovery, and state transitions.

Training, development/audit execution, and backtests do not increment serving-batch counters. The worker checks about every 15 seconds. **Review now** can claim 1–50 available signals through the same cooldown and pending-review guards. Fresh numerical departures can take the policy-review path below; healthy windows retain an observation.

## Human-only activation

Passing gates creates an eligible candidate. A human enters reviewer identity and clicks approve in the local frontend. The browser request requires the exact same loopback origin, explicit approval, and the expected-current release pointer. Keyed host clients remain supported. Approval is absent from agent tools and Workflow/Activity registration. A transaction checks evaluation gates and the pointer, records approval, and selects the release. Every revision requires a separate human decision. The local demo trusts its operator browser and provides no multi-user authentication.

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

The current milestone supports local binary CSV intake, packaging, fit/serve execution, measured evaluations, human approval, batch inference, and selected-node revisions. Further work includes broader improvement triggers, richer task/schema types, final-audit lifecycle management, distributed storage/images, authentication, and production deployment controls.


### Policy-aware review

Company-policy PDFs are versioned task knowledge. A lightweight training-range detector records aggregate novelty at batch completion. Reviews distinguish attributable runtime failures (one-node repair) from emerging-pattern hypotheses (cited policy proposal). The latter uses a separate Temporal workflow and existing four-tool agent, with Hindsight context. Both paths remain human controlled: a policy amendment publishes knowledge only; graph activation still requires evaluation and a separate human activation decision. See the README’s quick policy-learning demo.


### Frontend demo controllers

The Graph Runtime page offers a guided synthetic demo with dataset/policy cards, the serving DAG, and an evidence timeline. It uploads inputs and submits durable jobs with stable IDs, waits for human activation, runs two batches, and requests policy review. After the human publishes an amendment, the browser submits a one-node serving-output revision using the existing tuning API. Frozen evaluation and node-boundary checks apply. Human activation of the revision triggers the same emerging batch and a before/after recommendation comparison. The browser stores only progress metadata and resumes handoffs while the page is open. The 50-batch worker cadence operates independently.

The repeated-learning mode loads a prepared local bundle through `GET /api/learning-demo`, uploads five datasets, and submits four builds with stable round IDs. It advances after each completed evaluation, including failed gates, but stops on a failed build. Approval remains a separate operator action. Its chart, metric cards, public agent explanation, recorded model/tool activity, and source/config comparisons derive from retained jobs and releases.

The visible page polls every 3.5 seconds. Refresh restores browser progress, and a learning task URL can reconstruct the run from server jobs/datasets while its prepared bundle remains available. Admitted Temporal jobs continue independently of the page; subsequent browser-controlled handoffs require it to resume. **Start another demo** clears the selected browser progress and creates a new scope while retaining server evidence.
