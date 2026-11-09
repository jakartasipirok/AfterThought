#!/usr/bin/env python3
"""Run the three-way dispute benchmark with one command and one human approval."""

import argparse
import fcntl
import getpass
import hashlib
import json
import math
import os
import re
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import webbrowser
from pathlib import Path

from compare import http_json


ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
FEATURES = [
    "payment_amount", "refund_amount", "refund_status", "dispute_status",
    "hours_to_deadline", "evidence_complete", "order_found", "refund_count",
]


def env_file(path):
    """Read the repo's simple KEY=value .env without executing its contents."""
    values = {}
    if not path.exists():
        return values
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:]
        if "=" not in line:
            continue
        name, value = line.split("=", 1)
        name, value = name.strip(), value.strip()
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
            continue
        if value.startswith(('"', "'")) and value.endswith(value[0]):
            value = value[1:-1]
        else:
            value = value.split(" #", 1)[0].strip()
        values[name] = value
    return values


def command(args, env, *, capture=False):
    print("+", " ".join(str(part) for part in args), flush=True)
    return subprocess.run(args, cwd=ROOT, env=env, check=True, text=True,
                          capture_output=capture)


def benchmark_python(env):
    python = ROOT / ".venv" / "bin" / "python"
    if not python.exists():
        command([sys.executable, "-m", "venv", str(ROOT / ".venv")], env)
    if subprocess.run([str(python), "-c", "import sklearn"], cwd=ROOT,
                      env=env, stdout=subprocess.DEVNULL,
                      stderr=subprocess.DEVNULL).returncode != 0:
        command([str(python), "-m", "pip", "install", "-r",
                 str(HERE / "requirements.txt")], env)
    return python


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def request_json(url, payload):
    request = urllib.request.Request(url, data=json.dumps(payload).encode(),
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def upload_csv(url, path, options):
    boundary = "afterthought-bench-" + uuid.uuid4().hex
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"options\"\r\n\r\n"
        f"{json.dumps(options)}\r\n--{boundary}\r\n"
        f"Content-Disposition: form-data; name=\"file\"; filename=\"train.csv\"\r\n"
        f"Content-Type: text/csv\r\n\r\n"
    ).encode() + path.read_bytes() + f"\r\n--{boundary}--\r\n".encode()
    request = urllib.request.Request(url, data=body, headers={
        "Content-Type": "multipart/form-data; boundary=" + boundary,
    })
    with urllib.request.urlopen(request, timeout=120) as response:
        return json.load(response)


def wait_ready(base, server, timeout=120):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        if server.poll() is not None:
            raise RuntimeError("benchmark server exited during startup")
        try:
            health = http_json(base + "/api/health", timeout=4)
            if health.get("ready") and health.get("memory_backend") == "local":
                return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(1)
    raise TimeoutError("benchmark server did not become ready")


def wait_job(base, build_id, server, timeout):
    until = time.monotonic() + timeout
    last_phase = None
    while time.monotonic() < until:
        if server.poll() is not None:
            raise RuntimeError("benchmark server exited during authoring")
        detail = http_json(base + "/api/bootstraps/" + build_id)
        job = detail["job"]
        if job.get("phase") != last_phase:
            print("Authoring:", job.get("phase", job.get("status")), flush=True)
            last_phase = job.get("phase")
        if job["status"] == "failed":
            raise RuntimeError("graph build failed: " + job.get("error", "inspect the retained author session"))
        if job["status"] == "completed":
            release = detail.get("release")
            if not release or release.get("status") != "eligible":
                raise RuntimeError("candidate did not pass frozen evaluation gates; inspect the release in the UI")
            return release
        time.sleep(2)
    raise TimeoutError("graph authoring exceeded the timeout")


def wait_approval(base, release_id, server, timeout):
    until = time.monotonic() + timeout if timeout else None
    while until is None or time.monotonic() < until:
        if server.poll() is not None:
            raise RuntimeError("benchmark server exited while awaiting approval")
        release = http_json(base + "/api/releases/" + release_id)["release"]
        if release.get("approved_at"):
            return
        time.sleep(2)
    raise TimeoutError("human approval was not recorded before the timeout")


def build_objective(policy):
    return (
        "Build and test a binary ecommerce dispute triage graph. The verified "
        "meaning of review_required is defined by this explicitly fictional "
        "policy: " + policy + " Return decision 'escalate' for review_required=1 "
        "and 'accept' for review_required=0. Never return 'reject'. Ensure each "
        "decision agrees with its probability at threshold 0.5, and return "
        "a grounded reason. Do not make a fraud finding or approve a release."
    )


def saved_run(path, approach, test_csv, *, model=None, release_id=None):
    """Reuse a finished stage only when it belongs to this exact benchmark."""
    if not path.exists():
        return None
    run = json.loads(path.read_text())
    digest = hashlib.sha256(test_csv.read_bytes()).hexdigest()
    if (run.get("approach") != approach or run.get("status") != "completed"
            or run.get("dataset_test_sha256") != digest
            or not run.get("usage_complete")
            or (model is not None and run.get("model") != model)
            or (release_id is not None and run.get("release_id") != release_id)):
        raise RuntimeError(f"{path} is incomplete or does not match this run; refusing to repeat a possibly paid stage")
    return run


def launch(args, run_id, run_dir):
    resuming = args.resume_run is not None
    data_dir = run_dir / "data"
    config = env_file(ROOT / ".env")
    env = os.environ.copy()
    for name, value in config.items():
        if not env.get(name):
            env[name] = value
    if not env.get("AFTERTHOUGHT_MODEL_API_KEY"):
        if not sys.stdin.isatty():
            raise RuntimeError("Set AFTERTHOUGHT_MODEL_API_KEY in .env or the environment")
        env["AFTERTHOUGHT_MODEL_API_KEY"] = getpass.getpass("Model API key (not saved): ")
    if not env.get("AFTERTHOUGHT_MODEL_API_KEY"):
        raise RuntimeError("a model API key is required for full LLM and ZTA authoring")
    model = env.get("AFTERTHOUGHT_MODEL", "gpt-6.1-sol")
    env["AFTERTHOUGHT_MODEL"] = model
    metadata_path = run_dir / "run.json"
    if resuming and metadata_path.exists():
        metadata = json.loads(metadata_path.read_text())
        if metadata.get("model") != model:
            raise RuntimeError(f"resume requires the original model {metadata.get('model')}")
    elif not resuming:
        metadata_path.write_text(json.dumps({"run_id": run_id, "model": model}, indent=2) + "\n")
    env["AFTERTHOUGHT_MEMORY_BACKEND"] = "local"
    env["AFTERTHOUGHT_TASK_QUEUE"] = "afterthought-bench-" + run_id
    task = "dispute-bench-" + run_id
    python = benchmark_python(env)
    if not resuming:
        command([str(python), str(HERE / "generate_example.py"), "--out-dir",
                 str(data_dir), "--train", str(args.train), "--test", str(args.test)], env)
    policy = json.loads((data_dir / "policy.json").read_text())["policy"]
    train_csv, test_csv = data_dir / "train.csv", data_dir / "test.csv"
    conventional_path = run_dir / "conventional.json"
    if resuming:
        if saved_run(conventional_path, "conventional_model", test_csv) is None:
            raise RuntimeError("saved conventional result is missing; this run cannot resume")
    else:
        command([str(python), str(HERE / "compare.py"), "conventional", "--train",
                 str(train_csv), "--test", str(test_csv), "--out",
                 str(conventional_path)], env)

    command(["make", "up"], env)
    if subprocess.run(["docker", "image", "inspect", "afterthought-ml:v1"],
                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                      cwd=ROOT, env=env).returncode != 0:
        command(["make", "graph-runtime"], env)
    command(["make", "build"], env)
    port = args.port or free_port()
    base = f"http://127.0.0.1:{port}"
    log_path = run_dir / "server.log"
    server = None
    try:
        with log_path.open("a" if resuming else "w") as log:
            server = subprocess.Popen(
                [str(ROOT / "bin" / "afterthought"), "serve", "--addr", f"127.0.0.1:{port}"],
                cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT,
            )
            wait_ready(base, server)
            print("Benchmark runtime:", base, flush=True)
            build_id = "bootstrap-" + run_id
            if not resuming:
                options = {
                    "name": "Synthetic dispute triage " + run_id, "task": task,
                    "target": "review_required", "positive_label": "1",
                    "label_meaning": policy, "feature_columns": FEATURES,
                    "labels_verified": True, "synthetic": True,
                    "min_precision": 0.6, "min_recall": 0.6,
                    "max_false_positive_rate": 0.4, "threshold": 0.5,
                }
                dataset = upload_csv(base + "/api/datasets", train_csv, options)
                request_json(base + "/api/bootstraps", {
                    "id": build_id, "task": task, "dataset_id": dataset["id"],
                    "prompt": build_objective(policy), "max_turns": args.max_turns,
                })
            release = wait_job(base, build_id, server, args.author_timeout)
            release_id = release["id"]
            legacy_approved_before_resume = resuming and not metadata_path.exists() and bool(release.get("approved_at"))
            if release.get("task") != task:
                raise RuntimeError("saved release belongs to a different benchmark task")
            query = urllib.parse.urlencode({"task": task, "dataset": release["dataset_id"],
                                            "job": build_id, "release": release_id})
            review_url = base + "/graphs.html?" + query
            print("\nCandidate is eligible. Review its nodes and measured evidence:")
            print(review_url)
            print("Read the host approval code in another terminal with: bin/afterthought approval-code")
            print("Enter the code and your reviewer identity in the UI to approve this exact release.")
            print("Waiting for approval; this script will continue automatically.\n", flush=True)
            if not args.no_open_browser:
                webbrowser.open(review_url)
            wait_approval(base, release_id, server, args.approval_timeout)
            print("Human approval recorded. Running serving comparisons serially.", flush=True)
            llm_path = run_dir / "full-llm.json"
            llm_run = saved_run(llm_path, "full_llm", test_csv, model=model)
            if llm_run is None:
                if legacy_approved_before_resume:
                    raise RuntimeError("this older run was approved before resume and may have made paid full-LLM calls; refusing an automatic retry")
                marker = run_dir / "full-llm.started"
                if marker.exists():
                    raise RuntimeError("full LLM may have made paid calls before interruption; refusing an automatic retry")
                marker.write_text("Full LLM requests started; do not retry automatically if output is missing.\n")
                command([str(python), str(HERE / "compare.py"), "full-llm", "--test",
                         str(test_csv), "--policy", str(data_dir / "policy.json"),
                         "--model", model, "--concurrency", str(args.concurrency),
                         "--out", str(llm_path)], env)
                llm_run = saved_run(llm_path, "full_llm", test_csv, model=model)
                if llm_run is None:
                    raise RuntimeError("full LLM result is missing after execution")
            else:
                print("Reusing completed full LLM result; no new model calls.", flush=True)
            zta_path = run_dir / "zta.json"
            zta_run = saved_run(zta_path, "zta", test_csv, model=model, release_id=release_id)
            if zta_run is None:
                command([str(python), str(HERE / "compare.py"), "zta", "--server",
                         base, "--test", str(test_csv), "--release-id", release_id,
                         "--build-id", build_id, "--model", model, "--out",
                         str(zta_path)], env)
                if saved_run(zta_path, "zta", test_csv, model=model, release_id=release_id) is None:
                    raise RuntimeError("ZTA result is missing after execution")
            else:
                print("Reusing completed ZTA result.", flush=True)
            report_args = [str(python), str(HERE / "compare.py"), "report", "--test",
                           str(test_csv), "--full-llm", str(run_dir / "full-llm.json"),
                           "--conventional", str(run_dir / "conventional.json"),
                           "--zta", str(run_dir / "zta.json"), "--synthetic",
                           "--label-provenance", "Fictional policy in generate_example.py",
                           "--out", str(run_dir / "report.json")]
            if args.input_rate is not None:
                report_args.extend(["--input-rate", str(args.input_rate),
                                    "--output-rate", str(args.output_rate)])
            if args.compute_rate is not None:
                report_args.extend(["--compute-rate", str(args.compute_rate)])
            command(report_args, env)
            print("\nFinished. Results:", run_dir, flush=True)
            print("The case labels are synthetic; this does not measure fraud detection.")
            return run_dir
    finally:
        if server is not None and server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--train", type=int, default=800, help="synthetic training cases")
    parser.add_argument("--test", type=int, default=50, help="held-out cases / full LLM calls")
    parser.add_argument("--concurrency", type=int, default=8, help="parallel full LLM calls")
    parser.add_argument("--max-turns", type=int, default=20, help="ZTA authoring turn budget")
    parser.add_argument("--port", type=int, default=0, help="benchmark server port; 0 picks a free port")
    parser.add_argument("--author-timeout", type=int, default=1800, help="seconds to wait for candidate")
    parser.add_argument("--approval-timeout", type=int, default=0, help="seconds to wait for human approval; 0 waits until approved or interrupted")
    parser.add_argument("--resume-run", metavar="RUN_ID", help="continue a saved benchmark after interruption")
    parser.add_argument("--no-open-browser", action="store_true")
    parser.add_argument("--input-rate", type=float, help="model input USD per million tokens")
    parser.add_argument("--output-rate", type=float, help="model output USD per million tokens")
    parser.add_argument("--compute-rate", type=float, help="reserved compute USD per hour")
    args = parser.parse_args()
    if (args.input_rate is None) != (args.output_rate is None):
        parser.error("provide both model token rates or neither")
    if any(value is not None and (not math.isfinite(value) or value < 0)
           for value in (args.input_rate, args.output_rate, args.compute_rate)):
        parser.error("cost rates must be finite nonnegative numbers")
    if args.train < 50 or args.test < 1 or args.concurrency < 1 or args.concurrency > 32 or args.max_turns < 1 or args.max_turns > 32:
        parser.error("train >= 50, test >= 1, concurrency 1..32, max-turns 1..32 are required")
    if args.port < 0 or args.port > 65535 or args.approval_timeout < 0:
        parser.error("port must be 0..65535 and approval-timeout must be nonnegative")
    if args.resume_run is not None and not re.fullmatch(r"[0-9a-f]{12}", args.resume_run):
        parser.error("resume-run must be a 12-character lowercase hexadecimal run ID")
    run_id = args.resume_run or uuid.uuid4().hex[:12]
    run_dir = HERE / "results" / run_id
    try:
        if args.resume_run:
            if not run_dir.is_dir():
                raise RuntimeError(f"saved benchmark run does not exist: {run_dir}")
        else:
            run_dir.mkdir(parents=True)
        with (run_dir / ".launcher.lock").open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise RuntimeError("this benchmark run is already active") from error
            print("Benchmark run ID:", run_id, flush=True)
            print(f"To resume after an interruption: python3 benchmarks/disputes/run_all.py --resume-run {run_id}", flush=True)
            launch(args, run_id, run_dir)
    except (RuntimeError, TimeoutError, urllib.error.URLError, subprocess.CalledProcessError, ValueError) as error:
        print("Benchmark stopped:", error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
