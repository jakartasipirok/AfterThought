# AfterThought

The thinking already happened; now it just runs.

A Go/Temporal graph runtime and authoring harness that starts with **no domain-specific functions, rules, nodes, or seeded graphs**. The model sees exactly four tools: `read`, `bash`, `edit`, and `write`. From an uploaded labelled CSV, it authors separate fit and serving DAGs, with individually versioned Python nodes and tunable configuration.

The trusted supervisor snapshots node packages, trains on a frozen split, evaluates held-out predictions, and retains the evidence. **Passing evaluation never activates a release. Explicit human approval is required.** An approved serving graph processes a whole CSV batch using ordinary code/ML, with zero LLM calls or tokens. Hindsight supplies attributed learning evidence for later node improvements; it is not queried during serving.

## Run locally

Requires Go 1.25+, Docker with Docker Compose, and `make`. Node.js 20+ is needed only to run the frontend tests; the UI needs no install or build step. Start Docker Desktop, or run `colima start` if using Colima.

From the repository root, create the local configuration without overwriting an existing `.env`:

```sh
cp -n .env.example .env
```

Edit `.env` and set `AFTERTHOUGHT_MODEL_API_KEY`. Keep `AFTERTHOUGHT_MODEL=gpt-6.1-sol` and `AFTERTHOUGHT_MODEL_API=auto` to use Sol through the Responses API. Hindsight uses the same provider key and its separate `HINDSIGHT_MODEL` setting.

Then start the complete demo:

```sh
docker pull python:3.12-bookworm
make demo
```

Open **http://127.0.0.1:8080/graphs.html** for CSV-to-graph execution, or **http://127.0.0.1:8080** for the general authoring workspace. `make demo` starts Postgres, Temporal, and Hindsight, builds a generic Python/NumPy/pandas/scikit-learn environment and the Go binary, initializes the schemas, and runs the worker with the UI/API. The first startup downloads images and dependencies and may take several minutes. Keep this terminal running.

Press **Ctrl+C** to stop the UI and worker. Run `make down` to stop the supporting services while retaining their volumes. Run `make demo` again to restart with saved sessions and memory. Keep `.env`, `.afterthought/`, and the Docker volumes locally; Git ignores the configuration and generated state.

Service dashboards: Temporal at <http://127.0.0.1:8233> and Hindsight at <http://127.0.0.1:9999>.

See the [demo walkthrough](docs/demo.md) for a presentation sequence. The [Technical Design Document](docs/technical-design.md) covers architecture, contracts, recovery, automatic review, and design limitations.

1. Upload a CSV, declare the binary target, positive label, label meaning, permitted feature columns, and evaluation thresholds. Confirm label provenance; mark synthetic data explicitly. The server freezes training/development/audit membership and keeps audit data outside the authoring workspace.
2. Enter an objective and select **Author and evaluate candidate**. The agent creates `candidate.json` and self-contained source for explicit fit/serve nodes. The backend runs those actual nodes, fits only on training data, and computes precision, recall, F1, false-positive rate, accuracy, and majority baseline from held-out labels.
3. Inspect each node's pinned source/configuration, execution evidence, and evaluation reports. Select one node and request a revision. Every other node and the topology must remain unchanged; serving-node edits reuse the frozen fitted artifact. Candidates that regress in development F1 or fail gates are ineligible.
4. Review the eligible candidate, enter your name, and click **Approve and activate** in the local frontend. The click records your explicit decision. The API checks the same browser origin, evaluation gates, and expected current release, then records activation atomically. Each revision requires its own approval.
5. Upload an inference CSV and run an approved release. The graph processes the batch without a model or Hindsight client; nodes have no network or provider credentials. Download the predictions, reasons, node trace, and zero-token counters. Existing runs remain pinned when a later release is activated.

The general authoring page still supports prompts, optional dataset URLs, tool traces/files, human feedback, and Hindsight recall. Executable graph intake currently uses CSV upload, not automatic Kaggle ingestion.

The **claims replay** is a separate illustrative policy demo. Its fictional rules and browser decisions are not agent-authored or promoted workflows; see [replay details](docs/claims-replay.md). Live sessions do not import that ruleset. Real fraud accuracy requires externally verified labels; the bundled Kaggle extract has none. `CLAIM_STATUS` is excluded from graph features and targets.

This milestone supports binary tabular targets, up to 64 MiB/100,000 CSV rows, explicit feature selection, and a local Docker runtime. Numeric/boolean/string/null feature types are frozen at intake. Stratified row splits do not replace entity/time-based validation where those are needed. Audit rows are withheld from the agent, but repeated operator review of audit metrics consumes their independence; use fresh audit evidence for final claims. Automatic review handles attributable node failures, invalid outputs, and a bounded numerical novelty heuristic. Broader drift analysis, feedback-driven scheduling, shared image registries, multi-tenant authentication, and production claim settlement remain future work.

`bin/afterthought serve --with-worker=false` serves the UI/API with a separately running worker. Use `--addr 127.0.0.1:8081` or `--ui-dir frontend` to change local settings. The server accepts loopback bindings only; it is a local demo with access to agent execution, not an authenticated public service. API calls use the same origin and never send the provider key to the browser.

## Frontend learning demo

Choose **Repeated learning · your claims CSVs** for three cumulative learning rounds and a fixed accuracy benchmark. The [repeated-learning guide](docs/demo-learning-rounds.md) covers preparing the three local CSVs, starting/repeating entirely in the frontend, interpreting the measurements, and human activation.

Choose **Policy review · synthetic claims** for the policy-amendment demonstration below.

Open **Graph Runtime**, enter your name, and click **Start demo**. The page tells the story through **Dataset & knowledge**, **Workflow**, and **Learning & results**. The [60–90 second presenter script](docs/demo-90-seconds.md) includes preparation and narration.

PDF extraction needs Poppler (`brew install poppler` on macOS or `apt-get install poppler-utils` on Debian). Start the server with `make demo`. After backend changes, stop the previous server with Ctrl+C and run it again.

1. The frontend uploads 1,000 generated claims and the fictional company policy, then starts real graph authoring and evaluation.
2. Inspect the graph and measured evaluation, then click **Approve and activate**. Your reviewer name carries over from setup.
3. The page automatically runs an ordinary batch and an emerging-pattern batch, then requests an immediate review. The demo changes evidence reuse from 0–2 to 8–12. The normal scheduled cadence remains 50 batches.
4. Review the cited policy proposal, enter your assessment and note, and click **Approve policy amendment** or **Dismiss proposal**.
5. Publishing the policy triggers a one-node revision request in this guided demo. The agent uses the new policy to revise the serving output node. The page shows the changed source/configuration and actual before/after evaluation.
6. Click **Approve and activate** for the eligible revision. The same emerging batch then runs on the revised graph, with side-by-side recommendations and observed token counts.

**Demo options → Show technical workspace** exposes CSV uploads, source inspection, model/tool activity, manual node tuning, and review controls. Browser progress resumes in the same browser and origin. Keep the page open for automatic handoffs between durable server jobs.

Prepare authoring and evaluation before the talk. Leave the graph revision awaiting approval for a live decision during the presentation. Model latency determines preparation time. Equal before/after metrics mean classification stayed stable. The policy-driven revision demonstrates changed investigation behavior, and any measured accuracy improvement is shown directly.

The sample is synthesized from public NICB, Allianz, and Texas Department of Insurance guidance; [sources and editable policy](docs/demo-policy.md). It is fictional, not an insurer’s binding policy. The extended demo fields and generated labels are not claims about the Kaggle dataset. Numerical novelty means “investigate,” not “confirmed new fraud.”

## Automatic review every 50 batches

In **Graph Runtime**, enable **Auto-review** for the current learning scope after approving its initial graph. Enabling starts a fresh counter. One uploaded serving CSV is one batch, whether it contains 10 rows or 5,000. Completed and failed attempts count once per request ID. Each active release has its own count.

The worker checks every 15 seconds and consumes windows of 50 finished batches. At one batch per minute, a window takes about 50 minutes. At five batches per minute, it takes about 10 minutes. The panel shows progress and an estimate from observed completion intervals. Cooldown and pending proposals can delay the next review.

- Windows without actionable failures or fresh numerical departures record an observation without invoking the authoring model.
- A fresh numerical departure can request a cited policy amendment. Runtime repair takes priority when both signals occur. A human judges whether the pattern is suspicious, benign, or needs investigation.
- An attributable failure selects one serving node for repair. The agent receives node identity, aggregate missing-value counts, and attributed memory. The triggering batch stays in supervisor storage.
- Each repair has one authoring attempt with up to 12 turns. Repairs have a 10-minute cooldown and one pending automatic proposal per parent release.
- Candidates undergo frozen development/audit evaluation and a trusted replay of the failed batch. Development F1 must meet the incumbent's result.
- **Improvement ready for review** opens the changed node, before/after replay, and evaluation results. Use **Approve and activate** with your reviewer identity to activate it.
- Review triggers, outcomes, and attributed approvals enter the durable memory outbox. Disabling Auto-review stops future reviews. A repair already running may finish for human inspection.

For a controlled demo, use synthetic labelled data and an allowed missing value that causes an authored node to fail. A successful repair demonstrates robustness on that case. Fraud-accuracy improvements require verified labels and suitable independent evaluation.

Restart the UI and worker with `make demo` after updating the code. The toggle is disabled by default. The API exposes `GET /api/auto-review?task=YOUR_SCOPE` and `PUT /api/auto-review` with `{"task":"YOUR_SCOPE","enabled":true}`.

## CLI usage

The CLI loads `.env` automatically without overriding exported variables. Use the CLI instead of `make demo` when running a separate worker or submitting tasks from a terminal.

Set `AFTERTHOUGHT_MODEL_API_KEY` and `AFTERTHOUGHT_MODEL` for a model supporting function calling. The default endpoint is OpenAI; another compatible endpoint can be configured. GPT-6.1 Sol uses model ID `gpt-6.1-sol` and is automatically routed to the Responses API, which its tool calling requires. Other models retain the Chat Completions adapter. Override routing with `AFTERTHOUGHT_MODEL_API=responses` or `chat_completions` when needed.

The Responses adapter uses `store:false` and preserves opaque reasoning/output items in local transcripts across tool turns, so recovery does not depend on provider-stored response IDs. See the [official Sol model requirements](https://developers.openai.com/api/docs/models/gpt-6.1-sol).

```sh
colima start                       # if using Colima and it is stopped
docker pull python:3.12-bookworm    # generic Bash/Python environment
make up
make init
make graph-runtime
make worker
```

The model name can also be supplied with `bin/afterthought worker --model MODEL_NAME`. Credentials remain in the worker/Hindsight services and are not mounted into agent workspaces.

In another terminal:

```sh
bin/afterthought agent --task pipeline-research \
  --prompt 'Inspect the available skills, create a small reusable program and graph proposal from scratch, and run meaningful tests.'
```

Or supply a dataset:

```sh
bin/afterthought agent --task insurance-research \
  --dataset https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data/data \
  --objective 'Investigate claim-level fraud and propose an evaluated workflow'
```

Bash has no network by default. Dataset downloading needs `AFTERTHOUGHT_SANDBOX_NETWORK=bridge` in the worker's environment and a worker restart. The agent may also work with files explicitly placed in its workspace. It cannot access the host repository or its `.env`.

Sessions have a default budget of 12 model turns, configurable with `--max-turns` up to 32. Files and transcripts survive worker restarts. `bin/afterthought inspect --id SESSION_ID` shows the journal and model usage. Session files live under `.afterthought/workspaces/sha256(SESSION_ID)/`.

## Minimal visibility

The design follows [Pi's default four tools](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/index.ts) and [progressive skill loading](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md).

- `read`: bounded file reads, including lazy skill loading.
- `bash`: scripts, inspection, search, and tests inside a container.
- `edit`: replace exactly one matching text fragment.
- `write`: create or replace a workspace file atomically.

`bin/afterthought tools` prints the model-visible definitions. Skill bodies load on demand from the read-only mount. CSV sessions also stage a lazy `graph-authoring.md` protocol guide and permitted training/development data; no business implementation is seeded. Generated nodes expose `run(inputs, config)`; the trusted pinned wrapper implements artifact I/O and result checksums.

## Hindsight memory

`make up` starts Hindsight 1.2.0 alongside Postgres and Temporal. The Compose service uses the configured OpenAI key for extraction and embeddings, and RRF-only ranking to avoid downloading a large local reranker. Its API is at localhost:8888 and UI at localhost:9999. Temporal UI is at localhost:8233.

Hindsight is the default learning backend. Postgres retains the authoritative transcript/tool journal and raw memory outbox; Hindsight extracts and recalls learned patterns. Each task gets a separate bank, with task tags and source metadata. Derived observations include supporting source facts when available and are marked for verification; they do not inherit ground-truth status. No memories are pre-seeded.

Before authoring, the harness supplies recent attributed records plus relevant Hindsight recall. After a session, it retains command-outcome counts, execution failures, an explicitly unverified model summary, and the transcript digest. Raw tool output and dataset rows are not bulk-sent to memory. A completed session is not proof that its predictions are correct.

```sh
bin/afterthought feedback --task insurance-research --actor reviewer \
  --message 'A verified correction or an evaluation result, with its evidence.'
bin/afterthought memory --task insurance-research --query 'What failed, and why?'
bin/afterthought reflect --task insurance-research --query 'Which approaches have evidence of working?'
bin/afterthought sync-memory --task insurance-research
```

Retain is asynchronous. A stable document ID and operation UUID make outbox retries safe; acceptance does not mean processing has completed. Feedback remains available in the local journal while Hindsight processes it. If delivery fails after a run, the completed work remains saved and the outbox can be retried. A configured Hindsight recall failure is surfaced, not silently hidden.

`AFTERTHOUGHT_MEMORY_BACKEND=local` is an explicit offline mode for development. It does not provide Hindsight learning. Hindsight extraction, embeddings, and reflection may incur provider usage beyond the authoring-model usage shown in the session journal. They belong on the learning path and are excluded from graph serving.

## Verify

```sh
make test
make integration
```

Integration tests require the services and sandbox image. They use mock model/Hindsight endpoints where appropriate, plus real Docker, Postgres, and Temporal. Coverage includes four-tool visibility, lazy skills, workspace escapes, credential isolation, output limits/timeouts, tool-call journaling, turn budgets, and cross-session memory attribution.

The agent's Bash receives only the workspace and read-only skills. The Docker socket, host files, and provider credentials are excluded. Resource/time limits and an expired-container reaper bound work while the worker runs. Docker is a local demo boundary, not a production claim for arbitrary hostile code.

`bin/afterthought validate --graph graph.json` remains a structural validator for legacy v1 manifests. Executable CSV candidates use v2 `candidate.json` with fit/serve DAGs and are packaged/evaluated through the graph runtime UI/API. A plain `graph.proposal.json` remains an unresolved authoring artifact and cannot be served.

`make down` retains volumes. Preserve `.afterthought/` and the Postgres, Temporal, and Hindsight volumes. See [architecture](docs/architecture.md) and [dataset notes](docs/dataset-notes.md).
