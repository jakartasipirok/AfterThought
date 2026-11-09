import csv
import hashlib
import json
import os
import tempfile
import threading
import unittest
from argparse import Namespace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote
from unittest.mock import patch

from compare import cost, full_llm, report, score, zta
from run_all import build_objective, env_file


class ScoringTests(unittest.TestCase):
    def setUp(self):
        self.truth = [
            {"case_id": "a", "review_required": "1"},
            {"case_id": "b", "review_required": "0"},
            {"case_id": "c", "review_required": "1"},
            {"case_id": "d", "review_required": "0"},
        ]

    def test_invalid_and_missing_decisions_count_wrong(self):
        result = score(self.truth, [
            {"case_id": "a", "review_required": 1},
            {"case_id": "b", "review_required": None},
            {"case_id": "c", "review_required": 0},
        ], "case_id", "review_required")
        self.assertEqual(result["accuracy"], 0.25)
        self.assertEqual(result["coverage"], 0.5)
        self.assertEqual(result["recall"], 0.5)
        self.assertEqual(result["failures"], 2)

    def test_duplicate_ids_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "unique"):
            score(self.truth, [{"case_id": "a", "review_required": 1}] * 2, "case_id", "review_required")

    def test_setup_is_amortized_but_not_hidden(self):
        run = {
            "setup_input_tokens": 1000, "setup_output_tokens": 0,
            "serve_input_tokens": 0, "serve_output_tokens": 0,
            "setup_wall_ms": 3600000, "serve_wall_ms": 3600,
            "usage_complete": True,
        }
        result = cost(run, 100, 1, 1, 1)
        self.assertAlmostEqual(result["total_setup_usd"], 1.001)
        self.assertAlmostEqual(result["projected_total_usd"]["1000"], 1.011)
        provider_only = cost(run, 100, 1, 1, None)
        self.assertAlmostEqual(provider_only["model_api_batch_usd"], 0.001)
        self.assertIsNone(provider_only["total_batch_usd"])
        run["usage_complete"] = False
        self.assertIsNone(cost(run, 100, 1, 1, 1))

    def test_report_prices_default_model_without_inventing_compute_cost(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test = root / "test.csv"
            test.write_text("case_id,amount,review_required\na,10,1\n")
            digest = hashlib.sha256(test.read_bytes()).hexdigest()
            paths = {}
            for name in ("full_llm", "conventional_model", "zta"):
                run = {
                    "approach": name, "status": "completed", "dataset_test_sha256": digest,
                    "model": "RandomForestClassifier" if name == "conventional_model" else "gpt-6.1-sol",
                    "predictions": [{"case_id": "a", "review_required": 1}],
                    "setup_wall_ms": 1000, "serve_wall_ms": 1000,
                    "setup_input_tokens": 500_000 if name == "zta" else 0,
                    "setup_output_tokens": 0,
                    "serve_input_tokens": 1_000_000 if name == "full_llm" else 0,
                    "serve_output_tokens": 1_000_000 if name == "full_llm" else 0,
                    "usage_complete": True,
                }
                paths[name] = root / (name + ".json")
                paths[name].write_text(json.dumps(run))
            output = root / "report.json"
            report(Namespace(test=test, full_llm=paths["full_llm"],
                             conventional=paths["conventional_model"], zta=paths["zta"],
                             out=output, id_column="case_id", label_column="review_required",
                             input_rate=None, output_rate=None, compute_rate=None,
                             synthetic=True, label_provenance="fictional policy"))
            result = json.loads(output.read_text())
            self.assertEqual(result["rates"]["input_usd_per_million"], 2)
            self.assertEqual(result["approaches"]["full_llm"]["cost_estimate"]["model_api_batch_usd"], 12)
            self.assertEqual(result["approaches"]["zta"]["cost_estimate"]["model_api_batch_usd"], 1)
            self.assertIsNone(result["approaches"]["conventional_model"]["cost_estimate"]["total_batch_usd"])

    def test_full_llm_counts_real_response_usage_and_excludes_label(self):
        observed = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                observed.append(body)
                response = {
                    "status": "completed", "usage": {"input_tokens": 10, "output_tokens": 3},
                    "output": [{"type": "message", "content": [{"type": "output_text", "text": '{"review_required":1}'}]}],
                }
                encoded = json.dumps(response).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {"AFTERTHOUGHT_MODEL_API_KEY": "test-key"}):
                root = Path(directory)
                with (root / "test.csv").open("w", newline="") as output:
                    writer = csv.DictWriter(output, fieldnames=["case_id", "amount", "review_required"])
                    writer.writeheader()
                    writer.writerow({"case_id": "a", "amount": 10, "review_required": 1})
                (root / "policy.json").write_text(json.dumps({"policy": "Review all cases."}))
                args = Namespace(test=root / "test.csv", policy=root / "policy.json", out=root / "result.json", id_column="case_id", label_column="review_required", model="test-model", endpoint=f"http://127.0.0.1:{server.server_port}", timeout=2, max_errors=1)
                full_llm(args)
                result = json.loads(args.out.read_text())
                self.assertEqual(result["serve_input_tokens"], 10)
                self.assertEqual(result["predictions"], [{"case_id": "a", "review_required": 1}])
                sent_case = json.loads(observed[0]["input"][1]["content"])["case"]
                self.assertEqual(sent_case, {"amount": "10"})
        finally:
            server.shutdown()
            server.server_close()

    def test_zta_maps_hashed_batch_ids_and_counts_authoring_usage(self):
        uploaded = []
        revision_graph = {'id': 'graph-1'}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def send_json(self, value, status=200):
                encoded = json.dumps(value).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def do_GET(self):
                path = unquote(self.path)
                if path == "/api/releases/release-1":
                    self.send_json({"release": {"id": "release-1", "task": "benchmark", "dataset_id": "dataset-1", "approved_at": "2026-11-07T00:00:00Z", "graph": {'id': 'graph-1'}, "fit_artifact": {'digest': 'fit-1'}}})
                elif path == "/api/releases/release-2":
                    self.send_json({"release": {"id": "release-2", "task": "benchmark", "dataset_id": "dataset-1", "approved_at": "2026-11-07T00:00:00Z", "graph": revision_graph, "fit_artifact": {'digest': 'fit-1'}, "parent_release_id": "release-1", "policy_revision": {"source_release_id": "release-1", "post_evaluation": True, "evaluation_wall_ms": 12}}})
                elif path == "/api/datasets/dataset-1":
                    self.send_json({"feature_columns": ["amount"]})
                elif path == "/api/auto-review?task=benchmark":
                    self.send_json({"enabled": False})
                elif path == "/api/bootstraps/build-1":
                    self.send_json({"job": {"release_id": "release-1", "status": "completed", "created_at": "2026-11-07T00:00:00Z", "updated_at": "2026-11-07T00:00:02Z"}})
                elif path == "/api/sessions/internal/build/build-1/author/0":
                    self.send_json({"usage": [{"input_tokens": 20, "output_tokens": 10}]})
                elif path == "/api/sessions/internal/build/build-1/author/1":
                    self.send_json({"error": "missing"}, 404)
                elif path == "/api/health":
                    self.send_json({"memory_backend": "local"})
                elif path.startswith("/api/graph-runs/") and path.endswith("/output"):
                    csv_bytes = uploaded[0]
                    digest = "sha256:" + hashlib.sha256(csv_bytes).hexdigest()
                    row_id = "row-" + hashlib.sha256((digest + "/0").encode()).hexdigest()
                    self.send_json({"predictions": [{"row_id": row_id, "decision": "escalate"}]})
                elif path.startswith("/api/graph-runs/"):
                    self.send_json({"run": {"status": "completed"}})
                else:
                    self.send_json({"error": "unknown path"}, 404)

            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                marker = b"Content-Type: text/csv\r\n\r\n"
                uploaded.append(body.split(marker, 1)[1].rsplit(b"\r\n--afterthought-", 1)[0])
                self.send_json({"status": "queued"}, 202)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                with (root / "test.csv").open("w", newline="") as output:
                    writer = csv.DictWriter(output, fieldnames=["case_id", "amount", "review_required"])
                    writer.writeheader()
                    writer.writerow({"case_id": "a", "amount": 10, "review_required": 1})
                args = Namespace(test=root / "test.csv", out=root / "zta.json", id_column="case_id", label_column="review_required", server=f"http://127.0.0.1:{server.server_port}", release_id="release-1", build_id="build-1", model="test-model", timeout=2)
                zta(args)
                result = json.loads(args.out.read_text())
                self.assertEqual(result["predictions"], [{"case_id": "a", "review_required": 1}])
                self.assertEqual(result["setup_input_tokens"], 20)
                self.assertTrue(result["usage_complete"])
                self.assertEqual(uploaded, [b"amount\n10\n"])
                args.release_id = 'release-2'
                zta(args)
                revised = json.loads(args.out.read_text())
                self.assertEqual(revised['setup_wall_ms'], 2012)
                self.assertEqual(revised['policy_revision']['source_release_id'], 'release-1')
                revision_graph['id'] = 'changed-graph'
                with self.assertRaisesRegex(ValueError, 'retain the original graph'):
                    zta(args)
                self.assertEqual(len(uploaded), 2)
        finally:
            server.shutdown()
            server.server_close()

    def test_launcher_reads_local_config_without_executing_it(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text("# comment\nAFTERTHOUGHT_MODEL=gpt-6.1-sol\nAFTERTHOUGHT_MODEL_API_KEY='example key'\nIGNORED-LINE=x\n")
            values = env_file(path)
            self.assertEqual(values["AFTERTHOUGHT_MODEL_API_KEY"], "example key")
            self.assertNotIn("IGNORED-LINE", values)
            self.assertIn("Return decision 'escalate'", build_objective("Review open disputes."))


if __name__ == "__main__":
    unittest.main()
