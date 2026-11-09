# Historical insurance evaluation

Generate a single portable HTML report from `~/Downloads/fraud_oracle.csv`:

```sh
.venv/bin/python benchmarks/insurance/run.py
```

This prepares a fixed 500-claim sample, measures the conventional model, and
writes `benchmarks/insurance/results/latest/report.html`. The HTML embeds its
CSS, JavaScript, anonymized replay cases, and recorded results. It opens offline
and makes no external requests or model calls. Missing methods remain visibly pending.
Use **Play historical replay**, **Show all outcomes**, and the cost inputs to
present results. Print / PDF uses the browser's print dialog.

Run all three methods and update the same file:

```sh
.venv/bin/python benchmarks/insurance/run.py --run-all --test 500
```

This makes 500 paid per-claim model requests and additional authoring requests.
It uses the repository's model configuration, a separate local-memory worker,
and the real graph authoring/evaluation/approval/serving pipeline. Once a
candidate is eligible, open the printed review link, inspect it, and approve it
with your identity and the host code from `bin/afterthought approval-code`.
A new eligible release requires its own explicit human approval. If a candidate
fails runtime gates, the report retains its audit evidence and authoring cost,
marks final-test serving unavailable, and still runs the independent full LLM.
The script runs the
full LLM while awaiting approval and serves ZTA only after approval.

Reload the HTML after measurements finish. To regenerate it from saved results
without any paid calls:

```sh
.venv/bin/python benchmarks/insurance/run.py --report-only
```

An explicitly authorized demo policy revision can treat accuracy as diagnostic
while retaining all original precision, recall, FPR and prediction-validity gates:

```sh
make build
bin/afterthought release-triage --id ORIGINAL_RELEASE_ID --actor REVIEWER --message 'Explain why fraud triage requires these gates rather than majority accuracy.' --approve
.venv/bin/python benchmarks/insurance/run.py --serve-release NEW_RELEASE_ID
```

The first command uses the host-only approval key without printing it. It creates
a separate release, recomputes development/audit scores from retained artifacts,
and records the operator's rationale. Only a majority-accuracy rejection can be
reconsidered this way; other failures still block activation. The original
rejection, model, training data, and predictions remain intact. Without
`--approve`, the command only prepares the revised candidate. If a release is
already active in this task, pass its ID as `--expected-current-id`.
`--serve-release` runs only the approved graph; it makes no paid model calls and
retains the existing baseline measurements. The report discloses the
post-evaluation policy change. This is a demo acceptance decision, not proof of
production suitability. Future authored revisions still use the dataset's
original frozen policy unless separately reconsidered by an operator.

Run commands from the repository root. The Python environment is the same one
used by the dispute benchmark; install its requirements if needed. Go, Docker,
Compose, the generic runtime image (`make graph-runtime`), and `make` are
required for the live pipeline. Generated input, output and logs are ignored
by Git. The original source file stays in Downloads and is never modified.

## Evidence protocol

- **Target:** `FraudFound_P`, copied to `review_required` solely as the benchmark
  reference label. A prediction of 1 requests investigation; it does not establish
  fraud or authorize a claim action. Source label adjudication is not independently
  verified. Runtime label verification here means the declared benchmark reference
  matches the source field, not that the agent verified an insurer's investigation.
- **Training:** all 6,142 records from 1994.
- **Development/audit:** 1995 is divided deterministically by class into 2,597
  development and 2,598 private audit records. Explicit membership is passed via
  the new optional `split_column` intake field; default dataset behavior remains
  stratified 60/20/20. Membership is never a feature.
- **Final test:** 500 records sampled from 4,083 records in 1996 using a fixed hash
  ordering and proportional class allocation (26 positives). No performance-based
  selection, balancing to 50/50, or repeated test tuning. The reported source year
  is the time grouping; exact accident/claim date interpretation is not independently
  established. Replay order and arrival rate are presentation simulations.
- **Features:** an explicit intake-field allowlist removes policy/representative
  identifiers, demographic fields and fields with uncertain investigation-stage
  availability. Source year, target and anonymous case ID are excluded from serving
  features. A stronger deployment claim requires confirming availability of every
  remaining field at the actual decision time.
- **Conventional model:** fixed Random Forest, 200 trees, depth 10, minimum leaf 5,
  seed 20261107, one CPU job. It is intentionally fixed, not a tuned best-in-class
  conventional baseline. AfterThought may choose/tune a model on development data;
  any accuracy advantage therefore does not isolate architecture as its cause.
- **Full LLM:** one request per claim, same permitted feature values, with aggregate
  1994 training statistics. It does not receive final-test labels or a fitted model.
- **AfterThought:** an actual agent-authored graph using 1994 training and 1995
  development evidence, evaluated on private audit data, explicitly human approved.
- **Threshold:** 0.15 before final testing. Graph gates are precision ≥0.10,
  recall ≥0.15 and FPR ≤0.40. These are illustrative experiment settings, not an
  insurer-approved operating policy. The runtime also requires accuracy to beat
  the majority-class baseline; this additional gate can reject a model that meets
  the precision/recall/FPR settings. Any explicit operator revision must preserve
  the original result and disclose that the acceptance policy changed after evaluation.
- **Metrics:** fraud referrals matching dataset positives, missed positives, other
  claims referred, investigation load, accuracy, precision, recall, coverage,
  measured setup/serving times and recorded tokens. Missing decisions count wrong.
- **Cost:** default GPT-6.1 Sol Standard prices are stated assumptions ($2/M input,
  $10/M output, checked 2026-11-07). Editable compute cost starts blank. Total cost
  remains unavailable until supplied. Cost projections are linear scenarios, exclude
  human labor, and do not claim fraud losses prevented.
- **Previous experiment:** the existing synthetic dispute comparison appears in a
  separate collapsed section, never mixed with insurance metrics.

Rerunning `--run-all` reuses completed results and the original build. A lock
prevents duplicate launchers. Full LLM responses are saved individually as they complete;
an interrupted in-flight group stops automatic resumption to avoid silently
repeating possibly billed calls. Inspect that marker and explicitly decide
whether to retry it. An interrupted ZTA serving batch can be repeated (no model
calls); only the completed measurement is reported.

Verification: `python3 -m unittest discover -s benchmarks/insurance -p 'test_*.py'`
and `make test`. The latter covers explicit split validation and withholding
membership/labels from features. Tests do not require paid provider calls.

To explicitly archive an interrupted LLM attempt and pay for a fresh measurement,
add `--restart-llm` to `--run-all`. Prior attempt charges are excluded from the
fresh batch comparison and disclosed separately; partially lost usage cannot
be reconstructed as an exact invoice.
