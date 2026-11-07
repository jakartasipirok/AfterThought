# Repository Guidelines

## Project Structure & Module Organization

- `cmd/afterthought/`: CLI, worker, and demo server.
- `internal/harness/`: four tools, isolation, dataset requests, and skills.
- `internal/runtime/`: Temporal Workflow/Activities; `internal/model/`: provider transports.
- `internal/memory/`: Hindsight; `internal/storage/`: journal, schema, and artifacts; `internal/graph/`: contracts.
- `internal/webapi/`: HTTP bridge; `frontend/`: workspace and illustrative replay; `tests/`: browser-module tests.
- `harness/skills/`: read-only instructions; `docs/`: architecture and dataset notes.
- Go tests use adjacent `*_test.go` files. Binaries and state belong in ignored `bin/` and `.afterthought/`.

## Build, Test, and Development Commands

Requires Go 1.25+, Docker/Colima, and Compose.

- `make build`: build `bin/afterthought`.
- `make up` / `make down`: start/stop services, retaining volumes.
- `make init`: initialize empty storage; `make worker`: start the agent worker.
- `make demo`: start services, worker, UI, and API at `http://127.0.0.1:8080`.
- `make test`: run Go tests with the race detector and Node module tests.
- `make integration`: test real Docker/Postgres/Temporal with mock providers. Start services and pull `python:3.12-bookworm` first.
- `gofmt -w cmd internal` and `go vet ./...`: format and statically check Go code.

## Coding Style & Naming Conventions

Use `gofmt`, lowercase packages, PascalCase exports, and camelCase private identifiers. Version JSON contracts; document behavior/configuration changes.

## Testing Guidelines

Use Go `testing`, Temporal SDK utilities, and Node's test runner. Name Go tests `TestBehavior`; gate `TestIntegration...` with `AFTERTHOUGHT_INTEGRATION=1`. No coverage percentage is mandated. Prioritize replay, uncertain outcomes, memory provenance, idempotent submission, and workspace boundaries. Automated tests must not require paid calls.

## Architecture & Security

Expose exactly `read`, `bash`, `edit`, and `write` to the model. Start with an empty domain library; load skills lazily. Keep I/O/model calls in Activities and Workflow code deterministic. Preserve journal/outbox identities and distinguish Hindsight observations from verified human feedback. Never commit `.env`, credentials, raw dataset records, or session state.

## Commit & Pull Request Guidelines

History includes `feat: add FE`; use focused, imperative subjects. PRs need the problem, resulting behavior, validation, configuration changes, and relevant issues.

## Frontend Demo

- Serve the live workspace through `make demo`, using same-origin API calls. Keep provider credentials and opaque model response items out of browser responses.
- Keep the replay separate from agent-authored candidates. The initial domain library remains empty.
- Distinguish fictional policy and simulated failures. Kaggle data has no verified fraud label; exclude `CLAIM_STATUS` from screening. Review is not a fraud finding; Continue is not payment approval.
- Exclude source PII from extracts, screenshots, and prompts. Human investigators own claim decisions.
