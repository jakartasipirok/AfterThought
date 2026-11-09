"""Run and score three dispute-triage approaches on the same held-out cases.

All outputs use review_required: 1 means send to a human, 0 means routine.
An invalid or missing answer is wrong; no mode can silently drop hard cases.
"""

import argparse
import concurrent.futures
import csv
import hashlib
import io
import json
import math
import os
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime
from pathlib import Path


STANDARD_MODEL_RATES = {
    # Published OpenAI Standard text rates on 2026-11-07, USD per million tokens.
    "gpt-6.1-sol": (2.0, 10.0),
}
STANDARD_RATE_SOURCE = "https://developers.openai.com/api/docs/models/gpt-6.1-sol"


def read_cases(path, id_column, label_column):
    with Path(path).open(newline="") as source:
        reader = csv.DictReader(source)
        if not reader.fieldnames or id_column not in reader.fieldnames or label_column not in reader.fieldnames:
            raise ValueError(f"{path} needs {id_column} and {label_column} columns")
        rows = list(reader)
    if not rows:
        raise ValueError(f"{path} is empty")
    ids = [row[id_column] for row in rows]
    if any(not item for item in ids) or len(set(ids)) != len(ids):
        raise ValueError("case IDs must be unique and nonempty")
    if any(row[label_column] not in {"0", "1"} for row in rows):
        raise ValueError("held-out labels must be 0 or 1")
    return rows


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def elapsed_ms(start):
    return (time.perf_counter() - start) * 1000


def numeric_features(row, excluded):
    features = {}
    for key, value in row.items():
        if key in excluded:
            continue
        if value == "":
            features[key] = "__missing__"
            continue
        try:
            number = float(value)
            features[key] = number if math.isfinite(number) else "__missing__"
        except ValueError:
            features[key] = value
    return features


def conventional(args):
    try:
        from sklearn.ensemble import RandomForestClassifier
        from sklearn.feature_extraction import DictVectorizer
        from sklearn.pipeline import make_pipeline
    except ImportError as error:
        raise SystemExit("Install benchmarks/disputes/requirements.txt first") from error
    train = read_cases(args.train, args.id_column, args.label_column)
    test = read_cases(args.test, args.id_column, args.label_column)
    if set(train[0]) != set(test[0]):
        raise ValueError("training and held-out CSVs must have the same feature columns")
    if set(row[args.id_column] for row in train) & set(row[args.id_column] for row in test):
        raise ValueError("training and held-out case IDs overlap")
    excluded = {args.id_column, args.label_column}
    x_train = [numeric_features(row, excluded) for row in train]
    y_train = [int(row[args.label_column]) for row in train]
    if len(set(y_train)) != 2:
        raise ValueError("training data needs both classes")
    setup_start = time.perf_counter()
    model = make_pipeline(DictVectorizer(), RandomForestClassifier(
        n_estimators=100, max_depth=8, min_samples_leaf=3, random_state=20261107, n_jobs=1,
    ))
    model.fit(x_train, y_train)
    setup_ms = elapsed_ms(setup_start)
    serve_start = time.perf_counter()
    x_test = [numeric_features(row, excluded) for row in test]
    probabilities = model.predict_proba(x_test)
    positive_index = list(model[-1].classes_).index(1)
    predictions = [
        {"case_id": row[args.id_column], "review_required": int(float(score[positive_index]) >= 0.5)}
        for row, score in zip(test, probabilities)
    ]
    serve_ms = elapsed_ms(serve_start)
    write_json(args.out, {
        "approach": "conventional_model", "status": "completed",
        "dataset_test_sha256": hashlib.sha256(Path(args.test).read_bytes()).hexdigest(),
        "setup_wall_ms": setup_ms, "serve_wall_ms": serve_ms,
        "setup_input_tokens": 0, "setup_output_tokens": 0,
        "serve_input_tokens": 0, "serve_output_tokens": 0,
        "usage_complete": True, "model": "RandomForestClassifier",
        "predictions": predictions,
    })


def http_json(url, payload=None, timeout=120):
    body = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.load(response)


def response_text(response):
    parts = []
    for item in response.get("output", []):
        if item.get("type") == "message":
            for content in item.get("content", []):
                if content.get("type") == "output_text":
                    parts.append(content.get("text", ""))
    return "\n".join(parts)


def full_llm(args):
    key = os.getenv("AFTERTHOUGHT_MODEL_API_KEY")
    if not key:
        raise SystemExit("AFTERTHOUGHT_MODEL_API_KEY is required for full-LLM serving")
    parsed_endpoint = urllib.parse.urlparse(args.endpoint)
    if parsed_endpoint.scheme != "https" and not (parsed_endpoint.scheme == "http" and parsed_endpoint.hostname in {"localhost", "127.0.0.1", "::1"}):
        raise ValueError("model endpoint must use HTTPS or loopback HTTP")
    policy = json.loads(Path(args.policy).read_text())
    test = read_cases(args.test, args.id_column, args.label_column)
    concurrency = getattr(args, "concurrency", 1)
    if concurrency < 1 or concurrency > 32 or args.max_errors < 1:
        raise ValueError("concurrency must be 1..32 and max-errors must be positive")
    predictions = []
    input_tokens = output_tokens = 0
    usage_complete = True
    errors = 0

    def classify(row):
        features = {key: value for key, value in row.items() if key not in {args.id_column, args.label_column}}
        payload = {
            "model": args.model, "store": False, "max_output_tokens": 4096,
            "input": [
                {"role": "system", "content": "Classify the dispute triage case. Return only JSON with one integer field review_required, 0 or 1. Follow the supplied policy. Do not call tools or make a fraud finding."},
                {"role": "user", "content": json.dumps({"policy": policy["policy"], "case": features}, sort_keys=True)},
            ],
        }
        request = urllib.request.Request(
            args.endpoint.rstrip("/") + "/responses", data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + key},
        )
        record = {"case_id": row[args.id_column], "review_required": None}
        observed_input = observed_output = 0
        observed_usage = False
        try:
            with urllib.request.urlopen(request, timeout=args.timeout) as response:
                result = json.load(response)
            usage = result.get("usage", {})
            observed_usage = "input_tokens" in usage and "output_tokens" in usage
            observed_input = int(usage.get("input_tokens", 0))
            observed_output = int(usage.get("output_tokens", 0))
            answer = json.loads(response_text(result))
            if result.get("status") != "completed" or set(answer) != {"review_required"} or type(answer["review_required"]) is not int or answer["review_required"] not in (0, 1):
                raise ValueError("invalid or incomplete model decision")
            record["review_required"] = answer["review_required"]
        except (urllib.error.URLError, TimeoutError, ValueError, json.JSONDecodeError, KeyError) as error:
            record["error"] = type(error).__name__
        return record, observed_input, observed_output, observed_usage

    start = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        for offset in range(0, len(test), concurrency):
            for record, observed_input, observed_output, observed_usage in pool.map(classify, test[offset:offset + concurrency]):
                predictions.append(record)
                input_tokens += observed_input
                output_tokens += observed_output
                usage_complete = usage_complete and observed_usage
                errors += record["review_required"] is None
            if errors >= args.max_errors:
                break
    write_json(args.out, {
        "approach": "full_llm", "status": "completed" if len(predictions) == len(test) else "incomplete",
        "dataset_test_sha256": hashlib.sha256(Path(args.test).read_bytes()).hexdigest(),
        "setup_wall_ms": 0, "serve_wall_ms": elapsed_ms(start),
        "setup_input_tokens": 0, "setup_output_tokens": 0,
        "serve_input_tokens": input_tokens, "serve_output_tokens": output_tokens,
        "usage_complete": usage_complete, "model": args.model,
        "concurrency": concurrency, "predictions": predictions,
    })


def multipart_csv(csv_bytes, run_id):
    boundary = "afterthought-" + uuid.uuid4().hex
    chunks = [
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"options\"\r\n\r\n{json.dumps({'id': run_id})}\r\n".encode(),
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"held-out.csv\"\r\nContent-Type: text/csv\r\n\r\n".encode(),
        csv_bytes, f"\r\n--{boundary}--\r\n".encode(),
    ]
    return b"".join(chunks), boundary


def zta(args):
    base = args.server.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        raise ValueError("ZTA benchmark server must be loopback")
    test = read_cases(args.test, args.id_column, args.label_column)
    release_response = http_json(base + "/api/releases/" + urllib.parse.quote(args.release_id))
    release = release_response["release"]
    if not release.get("approved_at"):
        raise ValueError("ZTA release must already have human approval")
    dataset = http_json(base + "/api/datasets/" + urllib.parse.quote(release["dataset_id"]))
    review = http_json(base + "/api/auto-review?" + urllib.parse.urlencode({"task": release["task"]}))
    if review.get("enabled"):
        raise ValueError("disable automatic robustness review during the fixed comparison; its later authoring calls need separate cost accounting")
    features = dataset["feature_columns"]
    if any(name not in test[0] or name in {args.id_column, args.label_column} for name in features):
        raise ValueError("held-out CSV does not contain the release's exact feature set")
    if set(features) != set(test[0]) - {args.id_column, args.label_column}:
        raise ValueError("all approaches must see the same held-out feature columns")
    build = http_json(base + "/api/bootstraps/" + urllib.parse.quote(args.build_id))["job"]
    revision = release.get("policy_revision")
    source_id = revision.get("source_release_id") if revision else args.release_id
    if build.get("release_id") != source_id or build.get("status") != "completed":
        raise ValueError("build ID must identify the completed build of this release")
    if revision:
        source = http_json(base + "/api/releases/" + urllib.parse.quote(source_id))["release"]
        if (release.get('parent_release_id') != source_id or not revision.get('post_evaluation') or
                any(release.get(k) != source.get(k) for k in ('graph', 'fit_artifact', 'dataset_id', 'task'))):
            raise ValueError('policy revision must retain the original graph, fitted artifact, dataset, and task')
    usage = []
    for attempt in (0, 1):
        session_id = f"internal/build/{args.build_id}/author/{attempt}"
        try:
            session = http_json(base + "/api/sessions/" + urllib.parse.quote(session_id, safe=""))
        except urllib.error.HTTPError as error:
            if error.code == 404 and attempt == 1:
                break
            raise
        usage.extend(session.get("usage", []))
    if not usage:
        raise ValueError("authoring token usage is unavailable")
    health = http_json(base + "/api/health")
    memory_local = health.get("memory_backend") == "local"
    setup_input = sum(int(item["input_tokens"]) for item in usage)
    setup_output = sum(int(item["output_tokens"]) for item in usage)
    created = datetime.fromisoformat(build["created_at"].replace("Z", "+00:00"))
    updated = datetime.fromisoformat(build["updated_at"].replace("Z", "+00:00"))
    setup_ms = (updated - created).total_seconds() * 1000
    if revision:
        setup_ms += revision.get('evaluation_wall_ms', 0)
    start = time.perf_counter()
    output = io.StringIO(newline="")
    writer = csv.DictWriter(output, fieldnames=features, lineterminator="\n")
    writer.writeheader()
    writer.writerows({name: row[name] for name in features} for row in test)
    csv_bytes = output.getvalue().encode()
    csv_digest = "sha256:" + hashlib.sha256(csv_bytes).hexdigest()
    row_ids = ["row-" + hashlib.sha256((csv_digest + "/" + str(index)).encode()).hexdigest() for index in range(len(test))]
    id_to_case = dict(zip(row_ids, (row[args.id_column] for row in test)))
    run_id = "bench-" + uuid.uuid4().hex
    body, boundary = multipart_csv(csv_bytes, run_id)
    request = urllib.request.Request(
        base + "/api/releases/" + urllib.parse.quote(args.release_id) + "/runs",
        data=body, headers={"Content-Type": "multipart/form-data; boundary=" + boundary},
    )
    with urllib.request.urlopen(request, timeout=120) as response:
        json.load(response)
    deadline = time.monotonic() + args.timeout
    while True:
        run = http_json(base + "/api/graph-runs/" + run_id)["run"]
        if run["status"] in {"completed", "failed"}:
            break
        if time.monotonic() >= deadline:
            raise TimeoutError("ZTA batch did not finish within the benchmark timeout")
        time.sleep(0.25)
    if run["status"] != "completed":
        raise RuntimeError("ZTA batch failed: " + run.get("error", "unknown error"))
    graph_output = http_json(base + "/api/graph-runs/" + run_id + "/output")
    serve_ms = elapsed_ms(start)
    predictions = []
    for value in graph_output["predictions"]:
        decision = value.get("decision")
        predictions.append({
            "case_id": id_to_case.get(value.get("row_id")),
            "review_required": {"accept": 0, "escalate": 1}.get(decision),
            **({"error": "invalid_decision"} if decision not in {"accept", "escalate"} else {}),
        })
    write_json(args.out, {
        "approach": "zta", "status": "completed", "release_id": args.release_id,
        "build_id": args.build_id, "run_id": run_id,
        "policy_revision": revision,
        "dataset_test_sha256": hashlib.sha256(Path(args.test).read_bytes()).hexdigest(),
        "setup_wall_ms": setup_ms, "serve_wall_ms": serve_ms,
        "setup_input_tokens": setup_input, "setup_output_tokens": setup_output,
        "serve_input_tokens": 0, "serve_output_tokens": 0,
        "usage_complete": memory_local, "model": args.model,
        "note": "Serving token use is inferred from the no-model execution path, not from the API's unmetered zero counters. Hindsight cost is omitted unless memory_backend=local.",
        "predictions": predictions,
    })


def score(truth_rows, predictions, id_column, label_column):
    truth = {row[id_column]: int(row[label_column]) for row in truth_rows}
    submitted = {}
    for value in predictions:
        case_id = value.get("case_id")
        if case_id not in truth or case_id in submitted:
            raise ValueError("prediction IDs must be unique and belong to the held-out set")
        submitted[case_id] = value.get("review_required")
    tp = tn = fp = fn = 0
    failures = 0
    for case_id, actual in truth.items():
        predicted = submitted.get(case_id)
        if type(predicted) is not int or predicted not in (0, 1):
            failures += 1
            continue
        if actual == 1 and predicted == 1:
            tp += 1
        elif actual == 0 and predicted == 0:
            tn += 1
        elif predicted == 1:
            fp += 1
        else:
            fn += 1
    total = len(truth)
    precision = tp / (tp + fp) if tp + fp else 0
    positives = sum(truth.values())
    negatives = total - positives
    recall = tp / positives if positives else 0
    f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0
    return {
        "cases": total, "correct": tp + tn, "accuracy": (tp + tn) / total,
        "precision": precision, "recall": recall, "f1": f1,
        "balanced_accuracy": ((tp / positives if positives else 0) + (tn / negatives if negatives else 0)) / 2,
        "review_rate": (tp + fp) / total,
        "coverage": (total - failures) / total, "failures": failures,
        "true_positive": tp, "true_negative": tn, "false_positive": fp, "false_negative": fn,
    }


def cost(run, cases, input_rate, output_rate, compute_rate):
    if input_rate is None or output_rate is None or not run.get("usage_complete"):
        return None
    model_setup = (run["setup_input_tokens"] * input_rate + run["setup_output_tokens"] * output_rate) / 1_000_000
    model_serving = (run["serve_input_tokens"] * input_rate + run["serve_output_tokens"] * output_rate) / 1_000_000
    estimate = {
        "model_api_setup_usd": model_setup,
        "model_api_serving_usd": model_serving,
        "model_api_batch_usd": model_setup + model_serving,
        "total_setup_usd": None,
        "total_serving_usd": None,
        "total_batch_usd": None,
        "total_usd_per_case": None,
        "projected_total_usd": None,
    }
    if compute_rate is None:
        return estimate
    setup = model_setup + run["setup_wall_ms"] / 3_600_000 * compute_rate
    serving = model_serving + run["serve_wall_ms"] / 3_600_000 * compute_rate
    estimate.update({
        "total_setup_usd": setup, "total_serving_usd": serving,
        "total_batch_usd": setup + serving,
        "total_usd_per_case": (setup + serving) / cases,
        "projected_total_usd": {str(n): setup + serving / cases * n for n in (100, 1000, 10000)},
    })
    return estimate


def report(args):
    if (args.input_rate is None) != (args.output_rate is None):
        raise ValueError("provide both model token rates or neither")
    for rate in (args.input_rate, args.output_rate, args.compute_rate):
        if rate is not None and (not math.isfinite(rate) or rate < 0):
            raise ValueError("rates must be finite nonnegative numbers")
    test = read_cases(args.test, args.id_column, args.label_column)
    digest = hashlib.sha256(Path(args.test).read_bytes()).hexdigest()
    runs = {}
    raw_runs = {}
    model_names = {}
    for name, path in (("full_llm", args.full_llm), ("conventional_model", args.conventional), ("zta", args.zta)):
        run = json.loads(Path(path).read_text())
        if run.get("approach") != name or run.get("dataset_test_sha256") != digest:
            raise ValueError(f"{name} was not run against this exact held-out CSV")
        raw_runs[name] = run
        model_names[name] = run.get("model")
        metrics = score(test, run.get("predictions", []), args.id_column, args.label_column)
        metrics.update({
            "serve_wall_ms": run["serve_wall_ms"],
            "wall_ms_per_case": run["serve_wall_ms"] / len(test),
            "cases_per_second": len(test) * 1000 / run["serve_wall_ms"] if run["serve_wall_ms"] else None,
            "setup_wall_ms": run["setup_wall_ms"],
            "setup_input_tokens": run["setup_input_tokens"], "setup_output_tokens": run["setup_output_tokens"],
            "serve_input_tokens": run["serve_input_tokens"], "serve_output_tokens": run["serve_output_tokens"],
            "usage_complete": run["usage_complete"], "run_status": run["status"],
        })
        runs[name] = metrics
    if model_names["full_llm"] != model_names["zta"]:
        raise ValueError("full-LLM serving and ZTA authoring must use the same model for one token price")
    input_rate, output_rate = args.input_rate, args.output_rate
    rate_source = "user supplied" if input_rate is not None else None
    if input_rate is None and model_names["full_llm"] in STANDARD_MODEL_RATES:
        input_rate, output_rate = STANDARD_MODEL_RATES[model_names["full_llm"]]
        rate_source = "OpenAI Standard published 2026-11-07: " + STANDARD_RATE_SOURCE
    for name, run in raw_runs.items():
        runs[name]["cost_estimate"] = cost(run, len(test), input_rate, output_rate, args.compute_rate)
    result = {
        "synthetic": args.synthetic, "label_provenance": args.label_provenance,
        "test_sha256": digest, "cases": len(test), "rates": {
            "input_usd_per_million": input_rate,
            "output_usd_per_million": output_rate,
            "compute_usd_per_hour": args.compute_rate,
            "model_rate_source": rate_source,
        },
        "limitations": [
            "This reports decision accuracy on the provided labels, not fraud accuracy unless the labels are independently verified fraud outcomes.",
            "Wall time per case is batch wall time divided by case count, not individual latency.",
            "Serving wall times include each path's required transport: network API requests for LLM and ZTA, in-process prediction for the conventional model.",
            "ZTA setup time ends at an eligible candidate and excludes the required human approval wait.",
            "Model API USD uses published or supplied flat token rates and is an estimate, not an invoice; cached tokens, processing tier, and long context pricing can differ.",
            "Total USD requires a supplied compute-hour rate; a missing rate is not zero cost.",
            "Cost estimates exclude engineer time, human review, storage, network, and unmetered Hindsight calls.",
            "ZTA serving tokens are inferred from the no-model execution path; API zero counters are not telemetry. Hindsight must be disabled or separately priced.",
        ],
        "approaches": runs,
    }
    write_json(args.out, result)
    print("approach             accuracy  coverage  batch seconds  ms/case  model API USD  total USD")
    for name, value in runs.items():
        estimate = value["cost_estimate"]
        model_usd = f"{estimate['model_api_batch_usd']:.4f}" if estimate else "unavailable"
        total_usd = f"{estimate['total_batch_usd']:.4f}" if estimate and estimate["total_batch_usd"] is not None else "unavailable"
        print(f"{name:21}{value['accuracy']:8.3f}{value['coverage']:10.3f}{value['serve_wall_ms']/1000:15.3f}{value['wall_ms_per_case']:9.2f}  {model_usd:>13}  {total_usd:>11}")
    print(f"Full report: {args.out}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("conventional", "full-llm", "zta", "report"):
        command = sub.add_parser(name)
        command.add_argument("--test", required=True, type=Path)
        command.add_argument("--out", required=True, type=Path)
        command.add_argument("--id-column", default="case_id")
        command.add_argument("--label-column", default="review_required")
    conventional_command = sub.choices["conventional"]
    conventional_command.add_argument("--train", required=True, type=Path)
    llm = sub.choices["full-llm"]
    llm.add_argument("--policy", required=True, type=Path)
    llm.add_argument("--model", default=os.getenv("AFTERTHOUGHT_MODEL", "gpt-6.1-sol"))
    llm.add_argument("--endpoint", default=os.getenv("AFTERTHOUGHT_MODEL_BASE_URL", "https://api.openai.com/v1"))
    llm.add_argument("--timeout", type=int, default=120)
    llm.add_argument("--max-errors", type=int, default=3)
    llm.add_argument("--concurrency", type=int, default=1)
    zta_command = sub.choices["zta"]
    zta_command.add_argument("--server", default="http://127.0.0.1:8080")
    zta_command.add_argument("--release-id", required=True)
    zta_command.add_argument("--build-id", required=True)
    zta_command.add_argument("--model", default=os.getenv("AFTERTHOUGHT_MODEL", "gpt-6.1-sol"))
    zta_command.add_argument("--timeout", type=int, default=600)
    scorer = sub.choices["report"]
    scorer.add_argument("--full-llm", required=True, type=Path)
    scorer.add_argument("--conventional", required=True, type=Path)
    scorer.add_argument("--zta", required=True, type=Path)
    scorer.add_argument("--synthetic", action="store_true")
    scorer.add_argument("--label-provenance", required=True)
    scorer.add_argument("--input-rate", type=float)
    scorer.add_argument("--output-rate", type=float)
    scorer.add_argument("--compute-rate", type=float)
    args = parser.parse_args()
    if args.command == "conventional":
        conventional(args)
    elif args.command == "full-llm":
        full_llm(args)
    elif args.command == "zta":
        zta(args)
    else:
        report(args)


if __name__ == "__main__":
    main()
