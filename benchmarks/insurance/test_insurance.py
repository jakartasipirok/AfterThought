import csv
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from run import FEATURES, prepare, save, full_llm, serve_approved
from report import build_report


class InsuranceEvidenceTests(unittest.TestCase):
    def fixture(self, root):
        source = root / 'source.csv'
        with source.open('w', newline='') as f:
            writer = csv.DictWriter(f, fieldnames=[*FEATURES, 'Year', 'FraudFound_P', 'PolicyNumber', 'Sex'])
            writer.writeheader()
            for year in ('1994', '1995', '1996'):
                for i in range(100):
                    row = {k: 'fixture' for k in FEATURES}
                    row.update(Year=year, FraudFound_P=str(int(i % 10 == 0)), PolicyNumber='PRIVATE-SENTINEL', Sex='not-permitted')
                    row['Make'] = '</script><script>alert(1)</script>'
                    writer.writerow(row)
        return source

    def test_frozen_split_sanitization_and_sample_prevalence(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            source = self.fixture(root)
            original = source.read_bytes()
            out = root / 'run'
            m = prepare(source, out, 50)
            self.assertEqual(m['test_positive'], 5)
            members = []
            for name in ('train', 'development', 'audit', 'test'):
                with (out / (name + '.csv')).open() as f:
                    rows = list(csv.DictReader(f))
                self.assertNotIn('PolicyNumber', rows[0])
                self.assertNotIn('Sex', rows[0])
                self.assertNotIn('Year', rows[0])
                members.append({r['case_id'] for r in rows})
            for i, group in enumerate(members):
                for other in members[i+1:]: self.assertFalse(group & other)
            self.assertEqual(source.read_bytes(), original)
            self.assertEqual(prepare(source, out, 50), m)
            with self.assertRaises(ValueError): prepare(source, out, 40)

    def test_report_preserves_pending_and_escapes_source(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            out = root / 'run'
            prepare(self.fixture(root), out, 50)
            artifact = build_report(out)
            payload = json.loads((out / 'report.json').read_text())
            self.assertTrue(all(m['status'] == 'pending' and 'metrics' not in m for m in payload['methods']))
            html = artifact.read_text()
            self.assertNotIn('</script><script>alert(1)', html)
            self.assertNotIn('PRIVATE-SENTINEL', html)
            self.assertEqual(html.count('<script>'), 1)

    def test_report_rejects_changed_test_or_mismatched_results(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            out = root / 'run'
            prepare(self.fixture(root), out, 50)
            save(out / 'zta.json', {'approach': 'zta', 'dataset_test_sha256': 'different'})
            with self.assertRaisesRegex(ValueError, 'different evaluation'): build_report(out)
            (out / 'zta.json').unlink()
            with (out / 'test.csv').open('a') as f: f.write('\n')
            with self.assertRaisesRegex(ValueError, 'changed'): build_report(out)

    def test_rejected_candidate_never_becomes_a_final_test_result(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            out = root / 'run'
            prepare(self.fixture(root), out, 50)
            save(out / 'candidate.json', {'status': 'rejected', 'audit': {'metrics': {'accuracy': .9}}})
            build_report(out)
            payload = json.loads((out / 'report.json').read_text())
            candidate = next(m for m in payload['methods'] if m['key'] == 'zta')
            self.assertEqual(candidate['status'], 'rejected')
            self.assertNotIn('metrics', candidate)
            self.assertTrue(all(p is None for p in candidate['predictions']))

    def test_serving_requires_approval_without_paid_calls(self):
        with tempfile.TemporaryDirectory() as d:
            with patch('run.http_json', return_value={'release': {'status': 'eligible'}}), patch('run.zta') as serving, patch('run.full_llm') as paid:
                with self.assertRaisesRegex(ValueError, 'operator approval'):
                    serve_approved('http://127.0.0.1:9999', Path(d), {'run_id': 'fixture'}, 'release', 'model')
                serving.assert_not_called()
                paid.assert_not_called()

    def test_model_disconnect_is_recorded_and_saved_results_are_not_rebilled(self):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_): pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                requests.append(body)
                if len(requests) == 1:
                    self.close_connection = True
                    return
                data = json.dumps({'status': 'completed', 'usage': {'input_tokens': 12, 'output_tokens': 4},
                    'output': [{'type': 'message', 'content': [{'type': 'output_text', 'text': '{"review_required":0}'}]}]}).encode()
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as d, patch('run.render'):
                root = Path(d)
                out = root / 'run'
                manifest = prepare(self.fixture(root), out, 10)
                env = {'AFTERTHOUGHT_MODEL_API_KEY': 'test-key', 'AFTERTHOUGHT_MODEL': 'test-model',
                       'AFTERTHOUGHT_MODEL_BASE_URL': f'http://127.0.0.1:{server.server_port}'}
                full_llm(out, manifest, env, 2)
                result = json.loads((out / 'full-llm.json').read_text())
                self.assertEqual(result['status'], 'completed')
                self.assertEqual(len(result['predictions']), 10)
                self.assertEqual(sum(p['review_required'] is None for p in result['predictions']), 1)
                self.assertFalse(result['usage_complete'])
                self.assertEqual(result['serve_input_tokens'], 9 * 12)
                self.assertFalse((out / 'llm-inflight.json').exists())
                for request in requests:
                    claim = json.loads(request['input'][1]['content'])['claim']
                    self.assertEqual(set(claim), set(FEATURES))
                full_llm(out, manifest, env, 2)
                self.assertEqual(len(requests), 10)
        finally:
            server.shutdown()
            server.server_close()

if __name__ == '__main__': unittest.main()
