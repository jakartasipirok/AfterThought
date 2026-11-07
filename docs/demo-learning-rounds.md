# Repeated learning with supplied claims

## Run and repeat in the frontend

Prepare the bundle below once, start the app with `make demo`, then open <http://127.0.0.1:8080/graphs.html>. If the app is already running and the bundle exists, open the page directly.

1. Choose **Repeated learning · your claims CSVs** and enter your name.
2. Click **Start demo**. The frontend uploads the prepared splits and starts the initial build.
3. Keep the page open. Three additional rounds run automatically. Each adds labeled examples, tunes one fit node, retrains it, and evaluates the resulting candidate.
4. Read **Accuracy across learning rounds**. Click a graph version to read **Why this version changed**, then expand **Model responses and tool activity** for its recorded responses, commands, results, and token usage. The same selection shows its training and serving graph, source/configuration comparison, and approval eligibility.
5. Choose an eligible version, enter your reviewer name if necessary, and click **Approve and activate** to use it for serving.
6. Use **Demo options → Start another demo** to repeat in a fresh learning scope. Existing evidence stays available.

Model authoring takes several minutes per round. Prepare the four measured results before a short presentation. The table and changed node can be demonstrated in about one minute. Future runs can produce different models and scores.

## Data preparation

The local prepared bundle lives in ignored `.afterthought/learning-demo.json`. Node.js 20+ is required for preparation. To recreate it after resetting local state, run this once with your source files, then use the frontend.

```sh
node scripts/prepare_learning_demo.mjs \
  ~/Downloads/fraud_oracle.csv \
  ~/Downloads/new_fraud_only.csv \
  ~/Downloads/novel_pattern_fraud_only.csv
```

Preparation removes policy/representative identifiers and excludes `AddressChange_Claim` under the existing feature boundary. It deduplicates feature vectors across all three files, drops conflicting-label groups, and uses a deterministic shuffle. Source files remain intact.

The preparation command expects the supplied `FraudFound_P` schema with `0`/`1` labels and matching feature columns in all three files. It selects fraud examples from the challenge files and legitimate controls from the historical file. For other CSV schemas, use the [manual workspace](demo.md). If `AFTERTHOUGHT_DATA_DIR` points elsewhere, pass that directory's `learning-demo.json` path as the fourth argument.

| Input | Labeled rows | Purpose |
| --- | ---: | --- |
| Initial history | 400 | Bootstrap |
| More history | 600 | Round 1 |
| New fraud plus legitimate controls | 196 | Round 2 |
| Novel-pattern fraud plus legitimate controls | 140 | Round 3 |
| Fixed benchmark | 560 | Evaluate every version |

These counts describe the supplied files, after one duplicate is removed. The benchmark reserves 200 historical fraud, 50 new fraud, 30 novel-pattern fraud, and 280 legitimate cases before any learning split is created. Learning datasets use separate 60/20/20 train/development/audit splits. Each round adds only its training and development partitions. All audit partitions stay outside authoring. The benchmark uses its entire reserved set for evaluation.

The initial 400 claims split into **240 training, 80 development, and 80 audit cases**. With the supplied bundle, cumulative training counts are **240 → 600 → 716 → 800**. Training counts exclude development, audit, and benchmark cases.

## Reading the results

| Display | Meaning |
| --- | --- |
| Benchmark accuracy / main chart | Correct classifications divided by all 560 reserved benchmark cases. Every version uses the same cases. |
| Fraud caught | Correctly flagged fraud out of 280 fraud-labelled benchmark cases. Recall is that fraction as a percentage. |
| Legitimate claims flagged | False alarms out of 280 legitimate-labelled benchmark cases. Lower values mean fewer false alarms. |
| Original audit accuracy | Correct classifications on the same 80 audit cases reserved from the initial 400 claims. This checks performance on the original data after learning from additions. |
| Training rows | Cumulative rows used to fit the selected version. |
| Eligible for approval / Gate failed | Whether the original development/audit gates and revision checks passed. Expand the full results and selected version's failure details. |

For example, 263 fraud cases caught and 172 legitimate cases cleared gives `(263 + 172) / 560 = 77.7%` benchmark accuracy. Separately, 60 correct original audit cases gives `60 / 80 = 75%`. Actual results depend on the authored graph.

The benchmark is balanced. The original historical file has approximately 6% fraud, so the displayed accuracy describes this demo sample. Classification uses the frozen probability threshold of 0.5. The supplied labels and challenge provenance remain operator assertions. The round name **Novel pattern** identifies its input group; the model's explanation and measured results provide the evidence for assessing what changed.

Every candidate keeps the original dataset and serving topology. Additional training data requires a fit-node revision. The backend merges immutable training artifacts, reruns the actual graph, and computes metrics from reserved labels. Benchmark results stay out of agent prompts and memory. Hindsight retains attributed development observations.

The demonstration uses frozen original gates of precision ≥50%, recall ≥30%, false-positive rate ≤50%, majority-baseline accuracy, and the existing development-F1 nonregression check for revisions. These permissive demonstration thresholds are visible in the technical workspace. Benchmark metrics are comparative evidence. Failed gates and declining scores remain visible. Human approval controls activation.

A completed evaluation that fails a gate can still feed the next revision. A terminal build failure stops automatic progression for inspection. Each round uses the preceding candidate as its parent, and the supervisor checks development F1 against that parent. More training data can improve or reduce performance.

## Inspect the changes

Click a completed version's chart bar. **Why this version changed** shows the recorded public agent explanation. **View the instruction** shows the request for that round. **Model responses and tool activity** exposes recorded responses, tool arguments/results, and provider-reported token usage. **Inspect the changed training node** compares the actual source and configuration with its parent. The Training and Serving diagrams show how the revised node fits into the workflow.

The prepared sequence specifies data slices and requests one fit-node revision per round. The graph, selected model parameters, explanations, and scores are produced during execution. Original audit and benchmark labels/results stay outside authoring and improvement memory.

## Refresh, repeat, and Auto-review

- Keep the page open and visible for automatic handoffs. It polls about every 3.5 seconds. Refresh restores saved progress in the same browser and origin. Retain the `?task=learn-...` URL to reopen the experiment from its server records while its bundle and datasets remain available.
- Temporal continues an admitted job while the browser is closed. Reopening the page resumes the controller that submits subsequent rounds.
- **Start another demo** starts a fresh scope and retains earlier evidence. Reuse the prepared bundle. `make demo` is needed when starting or restarting the server.
- If **Start demo** fails, read the displayed error. A missing bundle requires the preparation command above. Use **Show technical workspace** to inspect failed jobs and model activity.
- Auto-review requires an approved active graph and counts finished serving CSV batches. Training and backtests here do not count. Its scheduled threshold is 50 batches; **Review now** can inspect available evidence sooner, subject to cooldown and pending-review guards.

Repeated backtesting supports iteration and comparison. Reserve a later untouched dataset for final generalization claims. Cold authoring latency and model variability make a prepared result preferable for the presentation.
