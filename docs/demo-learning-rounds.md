# Repeated learning with supplied claims

## Run and repeat in the frontend

Start the app with `make demo`, then open <http://127.0.0.1:8080/graphs.html>.

1. Choose **Repeated learning · your claims CSVs** and enter your name.
2. Click **Start demo**. The frontend uploads the prepared splits and starts the initial build.
3. Keep the page open. Three additional rounds run automatically. Each adds labeled examples, tunes one fit node, retrains it, and evaluates the resulting candidate.
4. Read **Accuracy across learning rounds**. Click a graph version to read **Why this version changed**, then expand **Model responses and tool activity** for its recorded responses, commands, results, and token usage. The same selection shows its training and serving graph, source/configuration comparison, and approval eligibility.
5. Choose an eligible version, enter your reviewer name if necessary, and click **Approve and activate** to use it for serving.
6. Use **Demo options → Start another demo** to repeat in a fresh learning scope. Existing evidence stays available.

Model authoring takes several minutes per round. Prepare the four measured results before a short presentation. The table and changed node can be demonstrated in about one minute. Future runs can produce different models and scores.

## Data preparation

The local prepared bundle lives in ignored `.afterthought/learning-demo.json`. To recreate it after resetting local state, run this once with your source files, then use the frontend.

```sh
node scripts/prepare_learning_demo.mjs \
  ~/Downloads/fraud_oracle.csv \
  ~/Downloads/new_fraud_only.csv \
  ~/Downloads/novel_pattern_fraud_only.csv
```

Preparation removes policy/representative identifiers and excludes `AddressChange_Claim` under the existing feature boundary. It deduplicates feature vectors across all three files, drops conflicting-label groups, and uses a deterministic shuffle. Source files remain intact.

| Input | Labeled rows | Purpose |
| --- | ---: | --- |
| Initial history | 400 | Bootstrap |
| More history | 600 | Round 1 |
| New fraud plus legitimate controls | 196 | Round 2 |
| Novel-pattern fraud plus legitimate controls | 140 | Round 3 |
| Fixed benchmark | 560 | Evaluate every version |

These counts describe the supplied files, after one duplicate is removed. The benchmark reserves 200 historical fraud, 50 new fraud, 30 novel-pattern fraud, and 280 legitimate cases before any learning split is created. Learning datasets use separate 60/20/20 train/development/audit splits. Each round adds only its training and development partitions. All audit partitions stay outside authoring. The benchmark uses its entire reserved set for evaluation.

## Reading the results

The benchmark is balanced. The original historical file has approximately 6% fraud, so its production prevalence differs substantially. Accuracy, fraud recall, false-alarm counts, and original-audit accuracy are displayed together. Classification uses the frozen probability threshold of 0.5. The supplied labels and challenge provenance remain operator assertions.

Every candidate keeps the original dataset and serving topology. Additional training data requires a fit-node revision. The backend merges immutable training artifacts, reruns the actual graph, and computes metrics from reserved labels. Benchmark results stay out of agent prompts and memory. Hindsight retains attributed development observations.

The demonstration uses frozen original gates of precision ≥50%, recall ≥30%, false-positive rate ≤50%, majority-baseline accuracy, and the existing development-F1 nonregression check for revisions. These permissive demonstration thresholds are visible in the technical workspace. Benchmark metrics are comparative evidence. Failed gates and declining scores remain visible. Human approval controls activation.

Repeated backtesting supports iteration and comparison. Reserve a later untouched dataset for final generalization claims. Cold authoring latency and model variability make a prepared result preferable for the presentation.
