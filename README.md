# AfterThought

The thinking already happened; now it just runs.

A Go/Temporal agent harness that starts with **no domain-specific functions, rules, nodes, or seeded graphs**. The model sees exactly four tools: `read`, `bash`, `edit`, and `write`. It creates its own candidate code and graph files from the user's task.

The harness provides durable authoring, isolated tool execution, progressive skill loading, attributed feedback, and **Hindsight learning memory**. Its browser workspace connects directly to these capabilities. Packaging generated nodes, evaluating and promoting releases, proactive evolution, and token-free graph serving are subsequent stages. This scaffold does not claim to solve fraud detection yet.

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

Open **http://127.0.0.1:8080**. `make demo` starts Postgres, Temporal, and Hindsight, builds the Go binary, initializes the schema, and runs the agent worker with the UI/API in one process. The first startup downloads service images and may take a few minutes. Keep this terminal running.

Press **Ctrl+C** to stop the UI and worker. Run `make down` to stop the supporting services while retaining their volumes. Run `make demo` again to restart with saved sessions and memory. Keep `.env`, `.afterthought/`, and the Docker volumes locally; Git ignores the configuration and generated state.

Service dashboards: Temporal at <http://127.0.0.1:8233> and Hindsight at <http://127.0.0.1:9999>.

See the [demo walkthrough](docs/demo.md) for a presentation sequence.

1. Submit a prompt, optionally with a dataset URL, task memory scope, and turn budget. The agent starts from its empty domain library.
2. Watch durable session progress, actual tool calls, token usage, generated files, and final output. Refreshing the page reconnects to retained sessions.
3. Save attributed human feedback under the same task scope. Recall Hindsight evidence, then start another session in that scope to use it. Retention is asynchronous; feedback does not automatically edit an existing workflow.

The **claims replay** link is a separate illustrative policy demo. Its fictional rules, simulated failures, and local review choices are not agent-authored or promoted workflows; see [replay details](docs/claims-replay.md). Live sessions do not import that ruleset. Dataset downloads still require enabling sandbox networking, and real fraud accuracy requires verified labels.

`bin/afterthought serve --with-worker=false` serves the UI/API with a separately running worker. Use `--addr 127.0.0.1:8081` or `--ui-dir frontend` to change local settings. The server accepts loopback bindings only; it is a local demo with access to agent execution, not an authenticated public service. API calls use the same origin and never send the provider key to the browser.

## CLI usage

The CLI loads `.env` automatically without overriding exported variables. Use the CLI instead of `make demo` when running a separate worker or submitting tasks from a terminal.

Set `AFTERTHOUGHT_MODEL_API_KEY` and `AFTERTHOUGHT_MODEL` for a model supporting function calling. The default endpoint is OpenAI; another compatible endpoint can be configured. GPT-6.1 Sol uses model ID `gpt-6.1-sol` and is automatically routed to the Responses API, which its tool calling requires. Other models retain the Chat Completions adapter. Override routing with `AFTERTHOUGHT_MODEL_API=responses` or `chat_completions` when needed.

The Responses adapter uses `store:false` and preserves opaque reasoning/output items in local transcripts across tool turns, so recovery does not depend on provider-stored response IDs. See the [official Sol model requirements](https://developers.openai.com/api/docs/models/gpt-6.1-sol).

```sh
colima start                       # if using Colima and it is stopped
docker pull python:3.12-bookworm    # generic Bash/Python environment
make up
make init
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

`bin/afterthought tools` prints the exact model-visible definitions. The initial prompt includes only skill names, descriptions, and paths. Full instructions are read on demand from the read-only skill mount. The supplied skill describes graph authoring and evaluation discipline; it contains no business implementation.

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

`AFTERTHOUGHT_MEMORY_BACKEND=local` is an explicit offline mode for development. It does not provide Hindsight learning. Hindsight extraction, embeddings, and reflection may incur provider usage beyond the authoring-model usage shown in the session journal. They belong on the learning path, not the future token-free serving path.

## Verify

```sh
make test
make integration
```

Integration tests require the services and sandbox image. They use mock model/Hindsight endpoints where appropriate, plus real Docker, Postgres, and Temporal. Coverage includes four-tool visibility, lazy skills, workspace escapes, credential isolation, output limits/timeouts, tool-call journaling, turn budgets, and cross-session memory attribution.

The agent's Bash receives only the workspace and read-only skills. The Docker socket, host files, and provider credentials are excluded. Resource/time limits and an expired-container reaper bound work while the worker runs. Docker is a local demo boundary, not a production claim for arbitrary hostile code.

The generic graph contract can be checked with `bin/afterthought validate --graph graph.json`. This checks structure and immutable references; it does not build packages, run evaluations, or promote a release. Unbuilt candidates should use `graph.proposal.json` and state unresolved artifacts explicitly.

`make down` retains volumes. Preserve `.afterthought/` and the Postgres, Temporal, and Hindsight volumes. See [architecture](docs/architecture.md) and [dataset notes](docs/dataset-notes.md).
