---
name: graph-workflow
description: Author or revise a reusable workflow graph, its node source, and tests. Use when creating a pipeline from a dataset or improving a prior workflow from evidence.
---

# Graph workflow authoring

Start by inspecting the task and available data. The domain-node library is empty. Create only the source and graph structure justified by this task. Reuse learned artifacts when the operator provides them.

1. Profile permitted data locally. Identify declared label semantics, available features, and missing evidence. Keep raw records, direct identifiers, and credentials out of model context. Never interpret an unexplained status code as verified fraud.
2. For an executable CSV bootstrap, read `graph-authoring.md` and `profile.json`. The harness stages training/development data, freezes split membership and evaluation policy, and withholds audit data. Feature values already have the profile's inferred JSON types.
3. Author `candidate.json` version `v2` with explicit `fit` and `serve` DAGs. Each node has a stable ID, one self-contained relative Python source path, declared dependencies/port bindings, JSON config, and limits. Separate serving transformations/scoring/routing into at least two meaningful connected nodes. Put tunable parameters into node config.
4. Each Python node exposes `run(inputs, config)` returning a JSON value. The trusted wrapper handles the artifact request/result protocol and checksums; do not implement filesystem envelopes in these modules. Fit only on training rows, freeze preprocessing and model state in the fit output, and use `$fit` when serving. The final prediction schema is defined in `graph-authoring.md`. Never use target labels as serving features.
5. Write and run meaningful tests. The supervisor snapshots source, wrapper, runtime image, and config into immutable per-node revisions, then executes the fit graph and evaluates the frozen serving graph. Report actual commands/outcomes. Passing tests alone does not establish predictive accuracy.
6. For node improvement, edit only the selected node's source/config in the staged incumbent. Preserve every other node, topology, binding, limit, and graph output. Development evidence can guide tuning; do not infer or access hidden audit labels. Repeatedly reviewing audit metrics consumes their independence.
7. Evaluation only makes a candidate eligible. Explicit human approval through the local operator frontend is required for activation. You cannot approve/promote. Existing runs stay pinned to their original graph/model digests.

When supplemental learning data is staged, read `learning.json` for provenance and counts. Use the cumulative training/development inputs and `data/original-development.json` to assess earlier behavior. A fit-node revision retrains on the expanded training input. Preserve its output interface for the existing serving nodes. Benchmark and audit evidence stays with the supervisor. Report measured development results, including regressions, and explain the selected-node change in short, plain-language statements.

For a plain authoring session without staged CSV data, `graph.proposal.json` is still appropriate for unresolved ideas. A proposal is not an executable release. Legacy `v1` manifests remain structurally validatable; executable CSV graphs use the v2 interface above.

Use `read` for files and skill instructions, `write` for new files, `edit` for exact changes, and `bash` for search, scripts, profiling, and tests. There are no domain-specific model tools.
