#!/usr/bin/env python3
"""Prepare, measure, and render the local historical insurance comparison."""
import argparse
import concurrent.futures
import csv
import fcntl
import hashlib
import http.client
import shutil
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE.parent / 'disputes'))
from compare import http_json, response_text, numeric_features, score, zta
from run_all import env_file, free_port, upload_csv, request_json, wait_ready

FEATURES = ['Month', 'WeekOfMonth', 'DayOfWeek', 'Make', 'AccidentArea',
            'DayOfWeekClaimed', 'MonthClaimed', 'WeekOfMonthClaimed', 'PolicyType',
            'VehicleCategory', 'VehiclePrice', 'Deductible', 'Days_Policy_Accident',
            'Days_Policy_Claim', 'PastNumberOfClaims', 'AgeOfVehicle', 'NumberOfCars', 'BasePolicy']
THRESHOLD = .15
MODEL_SYSTEM = ('Estimate whether this motor insurance claim should be referred for fraud investigation. '
                'Use only the supplied intake fields and historical training statistics. '
                'The threshold is an estimated probability of dataset-marked fraud >= 0.15. '
                'A referral is not a fraud finding, denial, or payment authorization. '
                'Return only JSON with one integer field review_required, 0 or 1. No tools.')


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix(path.suffix + '.tmp')
    temp.write_text(json.dumps(value, indent=2) + '\n')
    temp.replace(path)


def read_csv(path):
    with Path(path).open(newline='') as f:
        return list(csv.DictReader(f))


def write_csv(path, rows, fields):
    with Path(path).open('w', newline='') as f:
        w = csv.DictWriter(f, fieldnames=fields)
        w.writeheader()
        w.writerows(rows)


def stable_key(row):
    return hashlib.sha256(('insurance-v1/' + row['case_id']).encode()).hexdigest()


def stratified_sample(rows, n):
    if not 1 <= n <= len(rows):
        raise ValueError('sample must be between 1 and the number of 1996 claims')
    positive = [r for r in rows if r['review_required'] == '1']
    negative = [r for r in rows if r['review_required'] == '0']
    np = round(n * len(positive) / len(rows))
    return sorted(sorted(positive, key=stable_key)[:np] + sorted(negative, key=stable_key)[:n-np], key=stable_key)


def prepare(source, out, count):
    digest = hashlib.sha256(source.read_bytes()).hexdigest()
    if (out / 'manifest.json').exists():
        manifest = json.loads((out / 'manifest.json').read_text())
        if manifest['source_sha256'] != digest or manifest['test_count'] != count:
            raise ValueError('saved run uses a different source or sample size; choose another output directory')
        return manifest
    with source.open(newline='', encoding='utf-8-sig') as f:
        raw = list(csv.DictReader(f))
    if not raw or not set(FEATURES + ['FraudFound_P', 'Year']).issubset(raw[0]):
        raise ValueError('source is missing required insurance columns')
    if any(r['FraudFound_P'] not in ('0', '1') or r['Year'] not in ('1994', '1995', '1996') for r in raw):
        raise ValueError('unexpected source labels or years')
    years = defaultdict(list)
    for i, row in enumerate(raw):
        safe = {'case_id': f'CLM-{i+1:05d}', **{k: row[k] for k in FEATURES}, 'review_required': row['FraudFound_P']}
        years[row['Year']].append(safe)
    development, audit = [], []
    for label in ('0', '1'):
        group = sorted([r for r in years['1995'] if r['review_required'] == label], key=stable_key)
        midpoint = len(group) // 2
        development += group[:midpoint]
        audit += group[midpoint:]
    test = stratified_sample(years['1996'], count)
    out.mkdir(parents=True, exist_ok=True)
    fields = ['case_id', *FEATURES, 'review_required']
    for name, rows in [('train', years['1994']), ('development', development), ('audit', audit), ('test', test)]:
        write_csv(out / (name + '.csv'), rows, fields)
    dataset = [{**r, '_split': name} for name, rows in [('train', years['1994']), ('development', development), ('audit', audit)] for r in rows]
    write_csv(out / 'dataset.csv', dataset, [*fields, '_split'])
    context = {'training_claims': len(years['1994']), 'marked_fraud': sum(int(r['review_required']) for r in years['1994']), 'category_rates': {}}
    for feature in ['PolicyType', 'AccidentArea', 'VehiclePrice', 'PastNumberOfClaims', 'AgeOfVehicle']:
        groups = defaultdict(list)
        for r in years['1994']:
            groups[r[feature]].append(int(r['review_required']))
        context['category_rates'][feature] = {v: {'n': len(y), 'fraud_rate': round(sum(y) / len(y), 4)} for v, y in sorted(groups.items())}
    save(out / 'context.json', context)
    manifest = {
        'schema_version': 1, 'run_id': uuid.uuid4().hex[:12], 'source_name': source.name,
        'source_sha256': digest, 'test_sha256': hashlib.sha256((out / 'test.csv').read_bytes()).hexdigest(),
        'source_count': len(raw), 'source_positive': sum(int(r['FraudFound_P']) for r in raw),
        'test_count': len(test), 'test_positive': sum(int(r['review_required']) for r in test),
        'year_counts': {y: {'count': len(rows), 'positive': sum(int(r['review_required']) for r in rows)} for y, rows in years.items()},
        'split_counts': {'train': len(years['1994']), 'development': len(development), 'audit': len(audit)},
        'features': FEATURES, 'threshold': THRESHOLD,
        'label_provenance': 'FraudFound_P copied exactly from the supplied historical file: 1 = dataset-marked fraud. Insurer adjudication and feature availability have not been independently verified.',
        'excluded': [k for k in raw[0] if k not in FEATURES and k not in ['FraudFound_P', 'Year']],
        'sample_method': 'Fixed SHA-256 ordering with proportional class allocation; no selection using model performance.',
    }
    save(out / 'manifest.json', manifest)
    return manifest


def conventional(out, manifest):
    if (out / 'conventional.json').exists():
        return
    from sklearn.ensemble import RandomForestClassifier
    from sklearn.feature_extraction import DictVectorizer
    from sklearn.pipeline import make_pipeline
    start = time.perf_counter()
    train = read_csv(out / 'train.csv')
    excluded = {'case_id', 'review_required'}
    model = make_pipeline(DictVectorizer(), RandomForestClassifier(n_estimators=200, max_depth=10,
                      min_samples_leaf=5, random_state=20261107, n_jobs=1))
    model.fit([numeric_features(r, excluded) for r in train], [int(r['review_required']) for r in train])
    setup = (time.perf_counter()-start)*1000
    start = time.perf_counter()
    test = read_csv(out / 'test.csv')
    probabilities = model.predict_proba([numeric_features(r, excluded) for r in test])[:, list(model[-1].classes_).index(1)]
    predictions = [{'case_id': r['case_id'], 'review_required': int(p >= THRESHOLD), 'probability': float(p)} for r, p in zip(test, probabilities)]
    save(out / 'conventional.json', {'approach': 'conventional_model', 'status': 'completed',
          'model': 'RandomForestClassifier', 'dataset_test_sha256': manifest['test_sha256'],
          'setup_wall_ms': setup, 'serve_wall_ms': (time.perf_counter()-start)*1000,
          'setup_input_tokens': 0, 'setup_output_tokens': 0, 'serve_input_tokens': 0, 'serve_output_tokens': 0,
          'usage_complete': True, 'predictions': predictions, 'threshold': THRESHOLD})


def render(out):
    from report import build_report
    build_report(out)


def full_llm(out, manifest, env, concurrency):
    path = out / 'full-llm.json'
    model = env.get('AFTERTHOUGHT_MODEL', 'gpt-6.1-sol')
    test = read_csv(out / 'test.csv')
    context = json.loads((out / 'context.json').read_text())
    run = json.loads(path.read_text()) if path.exists() else {
        'approach': 'full_llm', 'status': 'running', 'model': model,
        'dataset_test_sha256': manifest['test_sha256'], 'setup_wall_ms': 0, 'serve_wall_ms': 0,
        'setup_input_tokens': 0, 'setup_output_tokens': 0, 'serve_input_tokens': 0,
        'serve_output_tokens': 0, 'usage_complete': True, 'predictions': [], 'concurrency': concurrency}
    if run['model'] != model or run['dataset_test_sha256'] != manifest['test_sha256']:
        raise ValueError('saved LLM run does not match this model and test set')
    if run['status'] == 'completed':
        return
    marker = out / 'llm-inflight.json'
    if marker.exists():
        raise RuntimeError('an interrupted group may already have incurred model charges; inspect llm-inflight.json before explicitly retrying')
    done = {r['case_id'] for r in run['predictions']}
    remaining = [r for r in test if r['case_id'] not in done]
    endpoint = env.get('AFTERTHOUGHT_MODEL_BASE_URL', 'https://api.openai.com/v1').rstrip('/')
    parsed = urllib.parse.urlparse(endpoint)
    if parsed.scheme != 'https' and not (parsed.scheme == 'http' and parsed.hostname in ('localhost', '127.0.0.1')):
        raise ValueError('model endpoint requires HTTPS or loopback HTTP')
    def classify(row):
        payload = {'model': model, 'store': False, 'max_output_tokens': 4096,
                   'input': [{'role': 'system', 'content': MODEL_SYSTEM},
                             {'role': 'user', 'content': json.dumps({'historical_training_statistics': context,
                              'claim': {k: row[k] for k in FEATURES}}, separators=(',', ':'))}]}
        req = urllib.request.Request(endpoint + '/responses', data=json.dumps(payload).encode(),
                headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + env['AFTERTHOUGHT_MODEL_API_KEY']})
        record = {'case_id': row['case_id'], 'review_required': None}
        usage = None
        try:
            with urllib.request.urlopen(req, timeout=120) as response:
                result = json.load(response)
            usage = result.get('usage')
            answer = json.loads(response_text(result))
            decision = answer.get('review_required')
            if result.get('status') != 'completed' or type(decision) is not int or decision not in (0, 1):
                raise ValueError('invalid model decision')
            record['review_required'] = decision
        except (OSError, http.client.HTTPException, ValueError, KeyError, TypeError) as error:
            record['error'] = type(error).__name__
            record['error_detail'] = str(getattr(error, 'reason', error))[:250]
            if isinstance(error, urllib.error.HTTPError): record['http_status'] = error.code
        return record, usage
    for offset in range(0, len(remaining), concurrency):
        chunk = remaining[offset:offset+concurrency]
        save(marker, {'case_ids': [r['case_id'] for r in chunk]})
        started = time.perf_counter()
        previous_ms = run['serve_wall_ms']
        outstanding = {r['case_id'] for r in chunk}
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = [pool.submit(classify, row) for row in chunk]
            for future in concurrent.futures.as_completed(futures):
                record, usage = future.result()
                run['predictions'].append(record)
                outstanding.remove(record['case_id'])
                if usage and 'input_tokens' in usage and 'output_tokens' in usage:
                    run['serve_input_tokens'] += usage['input_tokens']
                    run['serve_output_tokens'] += usage['output_tokens']
                else:
                    run['usage_complete'] = False
                run['serve_wall_ms'] = previous_ms + (time.perf_counter()-started)*1000
                run['status'] = 'completed' if len(run['predictions']) == len(test) else 'running'
                save(path, run)
                save(marker, {'case_ids': sorted(outstanding)})
        marker.unlink()
        render(out)
        print('LLM claims processed:', len(run['predictions']), '/', len(test), flush=True)
        if sum('error' in r for r in run['predictions']) >= 3:
            run['status'] = 'incomplete'
            save(path, run)
            render(out)
            raise RuntimeError('stopped after three model errors; saved failures remain visible')


def serve_approved(base, out, manifest, release_id, model):
    """Measure only an already approved release; never invoke paid models."""
    release = http_json(base + '/api/releases/' + urllib.parse.quote(release_id))['release']
    if not release.get('approved_at'):
        raise ValueError('release must already have explicit operator approval')
    build_id = 'insurance-build-' + manifest['run_id']
    # compare.zta checks the original build and exact policy-revision lineage.
    result_path = out / 'zta.json'
    if result_path.exists():
        recorded = json.loads(result_path.read_text())
        if recorded.get('release_id') != release_id:
            raise ValueError('saved ZTA measurement belongs to another release; choose a separate output directory')
    else:
        save(out / 'progress.json', {'status': 'serving', 'release_id': release_id, 'build_id': build_id})
        zta(SimpleNamespace(server=base, test=out / 'test.csv', out=result_path,
            id_column='case_id', label_column='review_required', release_id=release_id,
            build_id=build_id, model=model, timeout=600))
    result = json.loads(result_path.read_text())
    candidate_path = out / 'candidate.json'
    if candidate_path.exists() and not (out / 'original-candidate.json').exists():
        shutil.copy2(candidate_path, out / 'original-candidate.json')
    save(candidate_path, {
        'release_id': release_id, 'status': release['status'],
        'approved_at': release['approved_at'], 'approved_by': release['approved_by'],
        'development': release['development'], 'audit': release['audit'],
        'policy_revision': release.get('policy_revision'),
        **{k: result[k] for k in ('model', 'setup_wall_ms', 'setup_input_tokens', 'setup_output_tokens', 'usage_complete')}})
    save(out / 'progress.json', {'status': 'completed', 'release_id': release_id, 'build_id': build_id})
    render(out)
    print('APPROVED RELEASE MEASURED:', out / 'report.html', flush=True)


def execute(out, manifest, concurrency, serve_release=None):
    env = os.environ.copy()
    for key, value in env_file(ROOT / '.env').items():
        if not env.get(key): env[key] = value
    if not env.get('AFTERTHOUGHT_MODEL_API_KEY'):
        raise RuntimeError('set AFTERTHOUGHT_MODEL_API_KEY in .env to run model comparisons')
    model = env.get('AFTERTHOUGHT_MODEL', 'gpt-6.1-sol')
    task = 'insurance-bench-' + manifest['run_id']
    build_id = 'insurance-build-' + manifest['run_id']
    env.update(AFTERTHOUGHT_MEMORY_BACKEND='local', AFTERTHOUGHT_TASK_QUEUE=task)
    for command in (['make', 'up'], ['make', 'build']):
        subprocess.run(command, cwd=ROOT, env=env, check=True)
    port = free_port()
    base = f'http://127.0.0.1:{port}'
    with (out / 'server.log').open('a') as log:
        server = subprocess.Popen([str(ROOT / 'bin/afterthought'), 'serve', '--addr', f'127.0.0.1:{port}'],
                                  cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            wait_ready(base, server)
            if serve_release:
                serve_approved(base, out, manifest, serve_release, model)
                return
            try:
                detail = http_json(base + '/api/bootstraps/' + build_id)
            except urllib.error.HTTPError as e:
                if e.code != 404: raise
                dataset = upload_csv(base + '/api/datasets', out / 'dataset.csv', {
                    'name': 'Historical insurance benchmark', 'task': task, 'target': 'review_required',
                    'positive_label': '1', 'label_meaning': manifest['label_provenance'],
                    'feature_columns': FEATURES, 'split_column': '_split', 'labels_verified': True,
                    'synthetic': False, 'min_precision': .10, 'min_recall': .15,
                    'max_false_positive_rate': .40, 'threshold': THRESHOLD})
                request_json(base + '/api/bootstraps', {'id': build_id, 'task': task, 'dataset_id': dataset['id'], 'max_turns': 24,
                    'prompt': 'Author and evaluate a motor-insurance fraud-referral graph from the supplied historical dataset. '
                              'The binary target copies FraudFound_P from the source file; it is a benchmark label, not independently adjudicated insurer evidence. '
                              'Train on the frozen 1994 training split; use the 1995 development split to assess models. '
                              'Audit membership is withheld. A separate 1996 final test is never available during authoring. '
                              'Return calibrated or approximately calibrated fraud probabilities. Threshold is 0.15: escalate at or above it, accept below. '
                              'Never reject, deny, approve payment, or assert confirmed fraud. Use ordinary ML/code; handle unknown categories. '
                              'Select a useful classifier with development evidence and focused tests. Gates are minimum precision .10, recall .15 and maximum FPR .40. '
                              'Do not approve a release. No model or network calls during serving.'})
            until = time.monotonic() + 1800
            release = None
            while time.monotonic() < until:
                detail = http_json(base + '/api/bootstraps/' + build_id)
                job = detail['job']
                save(out / 'progress.json', {'status': 'authoring', 'phase': job.get('phase'), 'build_id': build_id})
                render(out)
                if job['status'] == 'failed':
                    raise RuntimeError('authoring failed: ' + job.get('error', 'inspect server log'))
                if job['status'] == 'completed':
                    release = detail.get('release')
                    if not release:
                        raise RuntimeError('completed build has no retained candidate')
                    break
                time.sleep(3)
            if not release: raise TimeoutError('authoring timed out')
            usage = []
            for attempt in (0, 1):
                sid = f'internal/build/{build_id}/author/{attempt}'
                try:
                    session = http_json(base + '/api/sessions/' + urllib.parse.quote(sid, safe=''))
                except urllib.error.HTTPError as error:
                    if error.code == 404 and attempt == 1: break
                    raise
                usage.extend(session.get('usage', []))
            created = datetime.fromisoformat(job['created_at'].replace('Z', '+00:00'))
            updated = datetime.fromisoformat(job['updated_at'].replace('Z', '+00:00'))
            save(out / 'candidate.json', {
                'release_id': release['id'], 'status': release['status'],
                'development': release.get('development'), 'audit': release.get('audit'),
                'model': model, 'setup_wall_ms': (updated-created).total_seconds()*1000,
                'setup_input_tokens': sum(x['input_tokens'] for x in usage),
                'setup_output_tokens': sum(x['output_tokens'] for x in usage), 'usage_complete': bool(usage)})
            if release['status'] != 'eligible':
                save(out / 'progress.json', {'status': 'rejected', 'release_id': release['id'],
                     'build_id': build_id, 'reason': 'Candidate failed frozen runtime gates; final-test serving was not authorized.'})
                render(out)
                full_llm(out, manifest, env, concurrency)
                render(out)
                print('EVALUATION COMPLETE; ZTA release rejected:', out / 'report.html', flush=True)
                return
            query = urllib.parse.urlencode({'task': task, 'dataset': release['dataset_id'], 'job': build_id, 'release': release['id']})
            review_url = base + '/graphs.html?' + query
            save(out / 'progress.json', {'status': 'awaiting_approval', 'review_url': review_url,
                 'release_id': release['id'], 'build_id': build_id,
                 'development': release.get('development'), 'audit': release.get('audit')})
            render(out)
            print('REVIEW:', review_url, flush=True)
            full_llm(out, manifest, env, concurrency)
            print('Waiting for human approval. Obtain host code with bin/afterthought approval-code.', flush=True)
            while True:
                current = http_json(base + '/api/releases/' + release['id'])['release']
                if current.get('approved_at'): break
                if server.poll() is not None: raise RuntimeError('benchmark server stopped')
                time.sleep(2)
            if not (out / 'zta.json').exists():
                zta(SimpleNamespace(server=base, test=out / 'test.csv', out=out / 'zta.json',
                    id_column='case_id', label_column='review_required', release_id=release['id'], build_id=build_id,
                    model=model, timeout=600))
            save(out / 'progress.json', {'status': 'completed', 'release_id': release['id'], 'build_id': build_id})
            render(out)
            print('COMPLETE:', out / 'report.html', flush=True)
        finally:
            server.terminate()
            try: server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, default=Path.home() / 'Downloads/fraud_oracle.csv')
    parser.add_argument('--out', type=Path, default=HERE / 'results/latest')
    parser.add_argument('--test', type=int, default=500)
    parser.add_argument('--run-all', action='store_true', help='run paid models and wait for explicit release approval')
    parser.add_argument('--report-only', action='store_true')
    parser.add_argument('--serve-release', help='measure an already approved release, without authoring or per-claim LLM calls')
    parser.add_argument('--restart-llm', action='store_true', help='archive a prior LLM attempt and explicitly pay for a fresh batch')
    parser.add_argument('--concurrency', type=int, default=16)
    args = parser.parse_args()
    if args.serve_release and (args.run_all or args.report_only or args.restart_llm):
        parser.error('--serve-release cannot be combined with --run-all, --report-only, or --restart-llm')
    if args.restart_llm and not args.run_all: parser.error('--restart-llm requires --run-all')
    if not 1 <= args.concurrency <= 32: parser.error('concurrency must be 1..32')
    args.out = args.out.resolve()
    args.out.mkdir(parents=True, exist_ok=True)
    with (args.out / '.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.restart_llm and ((args.out / 'full-llm.json').exists() or (args.out / 'llm-inflight.json').exists()):
            archive = args.out / 'prior-attempts' / uuid.uuid4().hex[:8]
            archive.mkdir(parents=True)
            for name in ('full-llm.json', 'llm-inflight.json', 'progress.json'):
                path = args.out / name
                if path.exists(): shutil.move(str(path), str(archive / name))
            print('Prior LLM attempt archived:', archive, flush=True)
        if args.report_only:
            render(args.out)
        else:
            manifest = prepare(args.source, args.out, args.test)
            conventional(args.out, manifest)
            render(args.out)
            if args.run_all or args.serve_release:
                try:
                    execute(args.out, manifest, args.concurrency, args.serve_release)
                except Exception as error:
                    progress_path = args.out / 'progress.json'
                    progress = json.loads(progress_path.read_text()) if progress_path.exists() else {}
                    progress.update(status='failed', error=str(error))
                    save(progress_path, progress)
                    render(args.out)
                    raise
    print('HTML report:', args.out / 'report.html', flush=True)

if __name__ == '__main__':
    main()
