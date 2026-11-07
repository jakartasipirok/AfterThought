import test from "node:test";
import assert from "node:assert/strict";
import { createLearningDemo } from "../src/learning-demo.mjs";
import { learningStory } from "../src/learning-story.mjs";
import { parseCSV, prepareLearningData } from "../src/learning-data.mjs";

test("CSV parsing supports BOM, quoted fields and missing final newline", () => {
  assert.deepEqual(parseCSV('\uFEFFa,b\r\n"x,y","z""q"'), [{ a: "x,y", b: 'z"q' }]);
  assert.throws(() => parseCSV('a,b\n"open,2'), /Unclosed/);
});

test("round preparation reserves disjoint balanced cases and removes identifiers", () => {
  const header = "PolicyNumber,RepNumber,AddressChange_Claim,signal,FraudFound_P\n";
  const csv = (start, count, label) => header + Array.from({ length: count }, (_, i) => `${start+i},1,no change,${start+i},${label ?? i%2}`).join("\n");
  const original = csv(0, 4000), newClaims = csv(10000, 149, 1), novel = csv(20000, 100, 1);
  const result = prepareLearningData(original, newClaims, novel);
  assert.deepEqual(result, prepareLearningData(original, newClaims, novel));
  const seen = new Set();
  for (const part of [result.baseline, result.benchmark, ...result.rounds]) {
    const rows = parseCSV(part.csv);
    assert.equal(rows.filter(row => row.FraudFound_P === "1").length, rows.length / 2);
    for (const row of rows) { assert.equal(seen.has(row.signal), false); seen.add(row.signal); assert.equal(row.PolicyNumber, undefined); }
  }
  assert.equal(result.benchmark.rows, 560);
});

function fixture() {
  const calls = [], jobs = new Map();
  let snapshot, saved, loseTune = false;
  const data = { baseline: { name: "baseline", csv: "x,label\n1,1" }, benchmark: { name: "benchmark", csv: "x,label\n2,1" },
    rounds: [1,2,3].map(i => ({ name: `round${i}`, csv: `x,label\n${i+2},1` })), featureColumns: ["x"], target: "label", positiveLabel: "1" };
  const report = accuracy => ({ passed: true, metrics: { accuracy, total: 100, true_positive: 40, true_negative: 40, false_positive: 10, false_negative: 10, recall: .8 } });
  async function api(path, options = {}) {
    calls.push({ path, ...options });
    if (path === "/learning-demo") return data;
    if (path === "/datasets") return { id: JSON.parse(options.body.get("options")).name };
    if (path.startsWith("/datasets/")) return { id: path.split("/").at(-1), rows: 100 };
    if (path === "/bootstraps" || path.endsWith("/tune")) {
      const index = Number(options.body.id.at(-1));
      const parent = index ? `release${index-1}` : undefined;
      const release = { id: `release${index}`, parent_release_id: parent, node_id: index ? "fit:train" : undefined,
        backtests: [report([.6,.75,.7,.8][index])], development: report(.8), audit: report(.8),
        graph: { fit: { output: "train", nodes: [{ id: "train" }] }, serve: { nodes: [] } } };
      jobs.set(options.body.id, { job: { id: options.body.id, status: "completed", session_id: `author-${index}` }, release });
      if (index && loseTune) { loseTune = false; throw new Error("Lost acknowledgement"); }
      return { id: options.body.id };
    }
    if (path.startsWith("/bootstraps/")) return jobs.get(path.split("/").at(-1));
    if (path.startsWith("/sessions/")) return { transcript: [{ role: "assistant", content: `Explanation for ${path.split("/").at(-1)} <model-choice>` }], tools: [], usage: [] };
    if (path.startsWith("/releases?")) return { active_release_id: "" };
    if (path.includes("/nodes/")) return { source: "model source", node: { config: {} } };
    throw new Error(path);
  }
  const controller = createLearningDemo({ api, save: record => { saved = structuredClone(record); }, select: async () => {}, changed: next => { snapshot = next; } });
  return { controller, calls, get snapshot() { return snapshot; }, get saved() { return saved; }, loseNextTune() { loseTune = true; } };
}

test("three cumulative rounds retain declines and never activate candidates", async () => {
  const f = fixture();
  await f.controller.start("Reviewer", "12345678");
  for (let i=0;i<4;i++) await f.controller.refresh();
  assert.equal(f.saved.finished, true);
  const tunes = f.calls.filter(call => call.path.endsWith("/tune"));
  assert.deepEqual(tunes.map(call => call.body.feedback_dataset_ids.length), [1,2,3]);
  assert.ok(f.calls.every(call => !call.path.endsWith("/approve") && !call.path.endsWith("/runs")));
  const html = learningStory(f.snapshot);
  assert.match(html, /70.0%/);
  assert.match(html, /\+20.0 percentage points/);
  assert.equal(f.snapshot.evidence.rounds.length, 4);
  await f.controller.inspect("release1");
  assert.equal(f.snapshot.evidence.release.id, "release1");
  assert.equal(f.saved.jobID, "learn-12345678-round-1");
  assert.equal(f.snapshot.evidence.activityID, "author-1");
  assert.match(learningStory(f.snapshot), /Explanation for author-1 &lt;model-choice&gt;/);
  assert.doesNotMatch(learningStory(f.snapshot), /Explanation for author-3/);
});

test("uncertain tune admission retries the same ID and preserves cumulative data", async () => {
  const f = fixture();
  await f.controller.start("Reviewer", "12345678");
  f.loseNextTune();
  await f.controller.refresh();
  assert.match(f.snapshot.error, /Lost acknowledgement/);
  await f.controller.refresh();
  const tunes = f.calls.filter(call => call.path.endsWith("/tune"));
  assert.deepEqual(tunes[0].body, tunes[1].body);
  assert.equal(f.saved.jobs.length, 2);
});
