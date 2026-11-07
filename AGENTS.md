# AfterThought agent entry point

Read [README.md](README.md) for the product story, five-stage workflow, data provenance, and run instructions. This file is the general entry point for coding agents working in this repository.

## Repository map

- `frontend/` — runnable browser prototype and sanitized motor-claim extract.
- `frontend/src/engine.mjs` — deterministic screening, business checks, routing, and disposition reconciliation.
- `frontend/src/app.mjs` — browser UI and local demo investigation decisions.
- `scripts/prepare_data.py` — rebuilds the PII-free extract from the three source CSVs.
- `tests/engine.test.mjs` — workflow and routing checks.

## Product boundary

Pitch: **Check the claim. Route exceptions. Close the loop.** The five stages are Screening → Automated Business Checking → Dynamic Routing → Investigation → Reconciliation. In this MVP, reconciliation means recording the claim disposition and next action. It does not match payments or accounting ledgers.

Keep source observations distinct from fictional policy and simulated failures. The Kaggle data has no verified fraud label. `CLAIM_STATUS` is excluded from screening, and Review is not a fraud finding. Continue is not payment approval. Do not add names, addresses, SSNs, bank fields, or other source PII to the frontend extract, screenshots, or LLM prompts.

The routine claim path makes zero LLM calls. Any future rule change needs fixed tests, replay evidence, and human approval before release. Human investigators own suspected-fraud escalation and claim decisions.

## Verification

Run `node --test tests/engine.test.mjs` from the repository root. Serve `frontend/` with `python3 -m http.server 8000 -d frontend` and check the UI in a browser.
