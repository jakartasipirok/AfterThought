# Repository Guidelines

## Project Structure

- `cmd/afterthought/`: CLI, worker, and local demo server.
- `internal/runtime/`, `model/`, `harness/`: durable authoring, providers, four tools, and isolation.
- `internal/dataset/`: CSV intake, private splits, evaluation; `execution/`: immutable node interpreter; `pipeline/`: bootstrap, tuning, approval, and batches.
- `internal/storage/`, `memory/`, `graph/`: journal/artifacts, Hindsight, and legacy contracts.
- `frontend/`: buildless UI; `harness/skills/`: read-only instructions; `harness/runtime/`: generic ML image; `docs/`: architecture/demo notes.
- Go tests are adjacent `*_test.go`; Node tests live in `tests/` and `frontend/tests/`.

## Development Commands

Requires Go 1.25+, Docker/Colima, Compose, and make. Node.js 20+ runs tests and data preparation; Poppler extracts policy PDFs.

- `make demo`: start services and serve graphs at `http://127.0.0.1:8080/graphs.html`.
- `make up` / `make down`: start/stop services, retaining volumes.
- `make build`, `make init`, `make worker`: build, initialize, or run separately.
- `make graph-runtime`: build the generic numerical image.
- `node scripts/prepare_learning_demo.mjs BASELINE.csv NEW.csv NOVEL.csv`: prepare the ignored learning bundle; see `docs/demo-learning-rounds.md`.
- `make test`: Go race tests and Node tests.
- `make integration`: real Docker/Postgres/Temporal with mock providers; requires services and runtime images.
- `gofmt -w cmd internal`, `go vet ./...`: formatting and static checks.

## Style and Tests

Use idiomatic Go: lowercase packages, PascalCase exports, camelCase private identifiers. Version JSON contracts and document behavior/configuration changes. Name tests `TestBehavior`; gate `TestIntegration...` with `AFTERTHOUGHT_INTEGRATION=1`. Prioritize replay, idempotency, artifact integrity, workspace boundaries, prediction validation, and approval rejection. Automated tests must not require paid calls.

## Architecture and Security

Expose exactly `read`, `bash`, `edit`, `write`. Seed no business nodes; load skills lazily. Keep I/O/model calls in Activities and Workflow code deterministic. Preserve journal/outbox identities and distinguish memory observations from verified human feedback.

Withhold audit data from authoring and labels from serving. Pin source/wrapper/image/config/model/input artifacts. Tuning preserves unselected revisions. Serving has no model/memory/network access. Evaluation never activates releases. Frontend approval requires a same-origin click and reviewer identity. Preserve gates, expected-current checks, and approval journals. Keyed host clients remain supported.

Keep cumulative training/development inputs separate from audit and benchmark data. Withhold benchmark reports from prompts/memory. Display retained metrics. Auto-review counts serving batches; the browser controls guided learning rounds.

Never commit secrets, raw datasets, or session state. Exclude source PII from prompts/screenshots. Keep fictional claims replay separate; exclude `CLAIM_STATUS` from screening. Review is not a fraud finding; recommendations are not settlement actions.

## Commits and Pull Requests

Use focused imperative subjects, such as `Document cumulative learning`. PRs explain the problem, behavior, validation, configuration changes, and relevant issues.
