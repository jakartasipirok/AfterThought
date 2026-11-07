---
name: graph-workflow
description: Author or revise a reusable workflow graph, its node source, and tests. Use when creating a pipeline from a dataset or improving a prior workflow from evidence.
---

# Graph workflow authoring

Start by inspecting the task and available data. The domain-node library is empty. Create only the source and graph structure justified by this task. Reuse learned artifacts when the operator provides them.

1. Profile the data locally. Identify candidate joins, label semantics, feature availability, and missing evidence. Keep direct identifiers and credentials out of model context. Do not interpret an unexplained status code as proven fraud.
2. Record the task hypothesis and assumptions in `plan.md`. Separate training, evaluation, and per-case serving work. A statistical model's inference can be token-free even though authoring uses an LLM.
3. Create node source and meaningful tests in the workspace. Use Bash to execute tests; report actual commands and outcomes. Avoid embedding raw data in prose.
4. Create `graph.proposal.json` if packages are not built. An unresolved proposal is not an executable release. Packaging, promotion, and automatic evolution are later harness stages in this first commit.
5. A finalized graph uses version `v1`, a name, an output node ID, and 1..16 nodes. Each node has `id`, immutable local `image` (`sha256:` plus 64 lowercase hex characters), `needs` (predecessor IDs), `inputs` (port name to `$input` or a declared predecessor), JSON `config`, `timeout_seconds` (1..300), and `max_attempts` (1..3). Graphs must be acyclic. No source or host commands belong in the manifest.
6. Packaged nodes will implement a fixed JSON request/result protocol: read `/input/request.json`, validate referenced input checksums, write `/output/output.json` and `/output/result.json`, then exit. The result includes `version`, `invocation_id`, and output `digest`/`size`. A trusted supervisor validates and commits results.
7. For improvement, preserve the incumbent artifacts and compare candidate results on development data plus a separate reserved audit set. Distinguish regressions, false positives/negatives, model token use, and runtime cost. Do not turn infrastructure failure into a business label.

Use `read` for files and skill instructions, `write` for new files, `edit` for exact changes, and `bash` for search, scripts, profiling, and tests. There are no domain-specific model tools.
