"""Render a portable report using recorded results; never synthesize model decisions."""
import csv
import hashlib
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / 'disputes'))
from compare import score


def build_report(out):
    out = Path(out)
    manifest = json.loads((out / 'manifest.json').read_text())
    test_path = out / 'test.csv'
    digest = hashlib.sha256(test_path.read_bytes()).hexdigest()
    if digest != manifest['test_sha256']:
        raise ValueError('test file changed after the evaluation split was frozen')
    with test_path.open(newline='') as f:
        rows = list(csv.DictReader(f))
    # Arrival order is a presentation simulation, independent of class-based sampling.
    rows.sort(key=lambda r: hashlib.sha256(('replay/' + r['case_id']).encode()).hexdigest())
    cases = [{'id': r['case_id'], 'label': int(r['review_required']),
              'vehicle': r['Make'], 'policy': r['BasePolicy'], 'area': r['AccidentArea']} for r in rows]
    methods = []
    for filename, key, name, description in [
        ('full-llm.json', 'full_llm', 'Full LLM', 'A model request for every claim'),
        ('conventional.json', 'conventional_model', 'Conventional ML', 'A fixed Random Forest trained once'),
        ('zta.json', 'zta', 'AfterThought', 'An agent-authored graph, approved for reuse')]:
        item = {'key': key, 'name': name, 'description': description, 'status': 'pending', 'predictions': [None] * len(rows)}
        path = out / filename
        if path.exists():
            run = json.loads(path.read_text())
            if run.get('dataset_test_sha256') != digest or run.get('approach') != key:
                raise ValueError(f'{filename} belongs to a different evaluation')
            metrics = score(rows, run.get('predictions', []), 'case_id', 'review_required')
            lookup = {r['case_id']: r.get('review_required') for r in run.get('predictions', [])}
            item.update(status=run['status'], model=run['model'], metrics=metrics,
                        processed=len(lookup), predictions=[lookup.get(r['case_id']) for r in rows])
            item['run'] = {k: run.get(k) for k in ['setup_wall_ms', 'serve_wall_ms', 'setup_input_tokens',
                'setup_output_tokens', 'serve_input_tokens', 'serve_output_tokens', 'usage_complete', 'concurrency', 'release_id']}
        candidate_path = out / 'candidate.json'
        if key == 'zta' and candidate_path.exists():
            item['candidate'] = json.loads(candidate_path.read_text())
            if item['candidate']['status'] == 'rejected' and item['status'] == 'pending':
                item['status'] = 'rejected'
        methods.append(item)
    progress = json.loads((out / 'progress.json').read_text()) if (out / 'progress.json').exists() else {'status': 'prepared'}
    payload = {'generated_at': datetime.now(timezone.utc).isoformat(), 'manifest': manifest, 'cases': cases, 'methods': methods, 'progress': progress,
               'rates': {'input': 2, 'output': 10, 'source': 'OpenAI Standard GPT-6.1 Sol rates, checked 2026-11-07'},
               'previous': None, 'prior_llm_attempts': len(list((out / 'prior-attempts').glob('*')))}
    previous = HERE.parent / 'disputes/results/c7991510d2d4/report.json'
    if previous.exists():
        prior = json.loads(previous.read_text())
        payload['previous'] = {'cases': prior['cases'], 'synthetic': prior['synthetic'], 'approaches': prior['approaches']}
    encoded = json.dumps(payload, separators=(',', ':'), ensure_ascii=True).replace('<', '\\u003c')
    template = (HERE / 'report.html').read_text()
    html = template.replace('/*__REPORT_DATA__*/null', encoded)
    temp = out / 'report.html.tmp'
    temp.write_text(html)
    temp.replace(out / 'report.html')
    (out / 'report.json').write_text(json.dumps(payload, indent=2) + '\n')
    return out / 'report.html'
