# Dispute triage comparison

This benchmark asks one operational question: **does this case need a human
reviewer?** `review_required=1` means review; `0` means routine handling. It
does not determine whether a customer committed fraud, whether to contest a
chargeback, or whether to move money.

## One-command launcher

From the repository root:

```sh
python3 benchmarks/disputes/run_all.py
```

The launcher creates a virtual environment if needed, generates 800 fictional
training and 50 held-out cases, runs the conventional baseline, starts Docker
services and a **separate benchmark worker** on a unique task queue, uploads
the training data, and authors a candidate. It opens the candidate in a
browser and waits while **you inspect and approve** it with the host approval
code. It then runs the full LLM and approved ZTA serving methods serially and
prints the common report. It waits for approval until you approve or stop the
script. The existing app on port 8080 remains available.

The launcher prints a run ID. If you stop it during the approval wait, resume
the same candidate and saved results with `python3 benchmarks/disputes/run_all.py
--resume-run RUN_ID`. Stop the original launcher before resuming. The resume
command restarts its isolated benchmark server and shows the review link again;
it does not generate new cases or author another candidate. It reuses a finished
full-LLM result only when the model and held-out file match. If full-LLM calls
started but no complete result was saved, the launcher stops rather than
possibly billing you for duplicate calls.

It loads the model key from `.env` or the environment; if neither has a key,
it prompts without saving the answer. The default 50 held-out cases mean 50
paid full-LLM requests. Use `--test 20` for a smaller smoke run or
`--test 200` for a larger comparison. For the default `gpt-6.1-sol` model,
the report estimates model API cost using [OpenAI's published Standard rates](https://developers.openai.com/api/docs/models/gpt-6.1-sol)
as of 2026-11-07: $2 per million input and $10 per million output tokens.
These are pricing assumptions, not an invoice. Supply `--input-rate` and
`--output-rate` together to override them. Add `--compute-rate` (USD per
reserved compute hour) to estimate total model plus compute cost; without
it, total USD is marked unavailable, not zero.

The launcher uses local memory and disables the optional 50-batch automatic
review for this fixed comparison. It keeps generated data and results in a
unique ignored folder under `benchmarks/disputes/results/` and stops its
benchmark server on exit. It does not submit approval for you or publish a
candidate that failed evaluation.

If you prefer to run each approach separately, use the steps below.

All three approaches receive the same held-out CSV, case IDs, and labels:

1. **Full LLM:** one Responses API call for every held-out case. It sees the
   case's selected fields and the written policy, never its label.
2. **Conventional model:** a fixed Random Forest trained on the training CSV.
   It makes no LLM calls. Its feature preparation and model settings are pinned
   in `compare.py`.
3. **AfterThought/ZTA:** the existing authoring workflow fits and evaluates an
   agent-authored graph, a human approves it, then an approved release serves
   the entire held-out CSV. The graph's final `accept` maps to 0 and `escalate`
   maps to 1; `reject` or any missing decision is counted wrong.

The conventional model is a deliberately strong, ordinary alternative. A
hand-written policy rule engine could be even stronger on the example below;
the example's labels are generated from those rules. A useful ZTA result must
justify the extra authoring machinery through adaptation, reviewability, or
repeated serving, not merely match the policy's labels.

## Dry run with fictional cases

The generator writes disjoint training and held-out CSVs plus its fictional
policy. This is **synthetic workflow evidence**, never a fraud accuracy claim.

```sh
python3 benchmarks/disputes/generate_example.py
python3 -m venv .venv
.venv/bin/pip install -r benchmarks/disputes/requirements.txt
.venv/bin/python benchmarks/disputes/compare.py conventional \
  --train benchmarks/disputes/example/train.csv \
  --test benchmarks/disputes/example/test.csv \
  --out benchmarks/disputes/results/conventional.json
```

For the full LLM run, use the same model as AfterThought authoring. Set
`AFTERTHOUGHT_MODEL_API_KEY` in your terminal; the runner never writes it to
results. It sends one case per call and stops after three API errors by default.
Set `--concurrency 8` if you want to compare a parallel full-LLM deployment;
record the value alongside the results. The default of 1 represents a single
request stream.

```sh
.venv/bin/python benchmarks/disputes/compare.py full-llm \
  --test benchmarks/disputes/example/test.csv \
  --policy benchmarks/disputes/example/policy.json \
  --out benchmarks/disputes/results/full-llm.json
```

For ZTA, start `make demo` with `AFTERTHOUGHT_MEMORY_BACKEND=local` so all
authoring model usage is visible in the journal; Hindsight's separate model
usage cannot otherwise be priced by this benchmark. Keep the new optional
automatic robustness review disabled during this fixed comparison; it can
start a separate authoring session after 50 finished batches and needs its own
adaptation-cost experiment. Upload `train.csv` at
`/graphs.html` with target `review_required`, positive label `1`, and these
feature columns:

```text
payment_amount,refund_amount,refund_status,dispute_status,hours_to_deadline,evidence_complete,order_found,refund_count
```

Mark the dataset **synthetic**, record the label meaning as the fictional
policy in `policy.json`, and set the evaluation gates before authoring. Give
the author this objective:

> Build a binary dispute triage graph using the supplied training data and the
> fictional policy in policy.json: mark review_required=1 for a missing order,
> refunds exceeding payment, an open dispute after a completed full refund,
> an open dispute with less than 48 hours remaining, or an open dispute with
> incomplete evidence. Return `escalate` for review and `accept` for routine;
> never return `reject`. The probability and decision must agree. Test the
> graph and explain the reason for each recommendation.

The operator must copy the policy text into the objective: `policy.json` is
not automatically uploaded to the authoring workspace. Inspect the candidate
and explicitly approve it. Then use its release ID and completed build ID:

```sh
.venv/bin/python benchmarks/disputes/compare.py zta \
  --test benchmarks/disputes/example/test.csv \
  --release-id RELEASE_ID --build-id BUILD_ID \
  --out benchmarks/disputes/results/zta.json
```

The ZTA runner uploads only the release's selected feature columns, waits for
the complete batch, downloads every decision, and maps the runtime's hashed
row IDs back to the held-out case IDs. Test labels are never uploaded.

Once all three runs exist:

```sh
.venv/bin/python benchmarks/disputes/compare.py report \
  --test benchmarks/disputes/example/test.csv \
  --full-llm benchmarks/disputes/results/full-llm.json \
  --conventional benchmarks/disputes/results/conventional.json \
  --zta benchmarks/disputes/results/zta.json \
  --synthetic \
  --label-provenance 'Fictional policy in generate_example.py' \
  --out benchmarks/disputes/results/report.json
```

Supply `--input-rate` and `--output-rate` (USD per million tokens) for other
models, and `--compute-rate` (USD per reserved compute hour) to estimate total
cost. The default Sol model rates are filled in as a stated assumption. If a
rate is missing, the corresponding dollar estimate says **unavailable**,
never `$0`. Use the rates for your actual model and hardware. The report
separates setup from serving and projects total cost at 100, 1,000, and
10,000 cases when the compute rate is available. It does not price engineering
time, human approval, storage, or network.

## Fairness rules

- Freeze the training/test split before any authoring or tuning. Reuse the
  exact test CSV for all methods. `report` checks its SHA-256 digest.
- Decide accuracy from the final review/routine decision on **every** case.
  Missing or malformed answers count wrong. Report coverage, precision,
  recall, and F1 beside accuracy because review cases may be rare.
- Compare batch wall time and throughput on the same machine and workload.
  `wall_ms_per_case` is throughput time divided by N, not individual response
  latency. Run several fresh batches before making a performance claim.
- Include ZTA authoring and conventional training as setup. Full LLM's
  per-case model tokens are serving cost. No approach gets free setup.
- Record provider prices and a compute rate for total USD; never mix token
  counts with USD or present unmetered Hindsight as free. The current ZTA serving path has no
  model client, but its API's zero counters are initialized values, not usage
  telemetry. The benchmark treats serving tokens as structurally zero and
  says so.
- The example gives the full LLM and the ZTA author the policy text; the
  conventional model learns from the labelled training CSV. This compares
  practical workflows, not equal-information learning algorithms. For a
  stronger claim, compare against a hand-written rules engine too. The
  conventional model trains on the whole example training CSV; AfterThought
  internally reserves development and audit rows, giving its author fewer
  rows. Record this difference when presenting results.

For real evidence, replace the synthetic files with cases whose review labels
were independently adjudicated before this experiment. Preserve the original
class distribution, split by time or merchant where relevant, document when
each feature was available, and keep source PII out of LLM calls.
