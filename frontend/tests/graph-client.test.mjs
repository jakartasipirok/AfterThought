import test from "node:test";
import assert from "node:assert/strict";
import { evaluationGates, servingCounters, predictionPreview, graphLayout, featureColumns, createGraphAPI } from "../src/graph-client.mjs";

test("evaluation compares measured precision, recall, and false positives to the frozen report policy", () => {
  const report = { policy: { min_precision: 0.9, min_recall: 0.7, max_false_positive_rate: 0.1 }, metrics: { precision: 0.89, recall: 0.7, false_positive_rate: 0 } };
  const gates = evaluationGates(report, { min_precision: 0.1, min_recall: 0.1, max_false_positive_rate: 1 });
  assert.deepEqual(gates.map((gate) => [gate.name, gate.status]), [["Precision", "failed"], ["Recall", "passed"], ["False positive rate", "passed"]]);
  assert.equal(gates[0].limit, 0.9);
});

test("missing evaluation numbers remain unknown rather than passing a gate", () => {
  const gates = evaluationGates({ metrics: {} }, { min_precision: 0, min_recall: 0, max_false_positive_rate: 1 });
  assert.ok(gates.every((gate) => gate.status === "unknown"));
});

test("zero-token serving requires all actual counters to report zero", () => {
  assert.equal(servingCounters({ model_calls: 0, input_tokens: 0, output_tokens: 0 }).verifiedZero, true);
  assert.equal(servingCounters({ model_calls: 0 }).verifiedZero, false);
  assert.equal(servingCounters({ model_calls: 0, input_tokens: 0, output_tokens: 2 }).verifiedZero, false);
  assert.equal(servingCounters({}).calls, null);
});

test("prediction preview reflects real probabilities and recommendations while excluding source columns", () => {
  const rows = predictionPreview([{ row_index: 12, probability: 0, decision: "accept", reasons: ["below declared threshold"], SSN: "not-for-ui", full_name: "not-for-ui" }]);
  assert.deepEqual(rows, [{ row: 12, score: 0, decision: "accept", reasons: "below declared threshold" }]);
  assert.doesNotMatch(JSON.stringify(rows), /SSN|full_name|not-for-ui/);
  assert.equal(predictionPreview([{ row_id: "row-17", probability: 0.9, decision: "escalate" }])[0].row, "row-17");
  assert.equal(predictionPreview(Array.from({ length: 120 }, () => ({ probability: 0.8, decision: "escalate" }))).length, 100);
});

test("explicit DAG edges come from dependencies, including parallel branches", () => {
  const graph = { output: "combine", nodes: [{ id: "combine", needs: ["left", "right"] }, { id: "left", needs: ["input"] }, { id: "right", needs: ["input"] }, { id: "input", needs: [] }] };
  const layout = graphLayout(graph);
  assert.equal(layout.invalid, false);
  assert.equal(layout.edges.length, 4);
  const positions = new Map(layout.nodes.map((node) => [node.id, node]));
  assert.ok(positions.get("input").x < positions.get("left").x);
  assert.equal(positions.get("left").x, positions.get("right").x);
  assert.notEqual(positions.get("left").y, positions.get("right").y);
  assert.ok(positions.get("combine").x > positions.get("right").x);
  assert.equal(graphLayout({ nodes: [{ id: "bad", needs: ["missing"] }] }).invalid, true);
  assert.equal(graphLayout({ nodes: [{ id: "a", needs: ["b"] }, { id: "b", needs: ["a"] }] }).invalid, true);
});

test("feature declarations preserve explicit order and remove empty duplicates", () => {
  assert.deepEqual(featureColumns(" amount, days, amount, , cost "), ["amount", "days", "cost"]);
});

test("multipart requests preserve the browser boundary and JSON options without changing the file", async () => {
  const body = new FormData();
  body.append("file", new Blob(["x,label\n1,yes\n"], { type: "text/csv" }), "evidence.csv");
  body.append("options", JSON.stringify({ task: "test", labels_verified: true }));
  const api = createGraphAPI(async (url, options) => {
    assert.equal(url, "/api/datasets");
    assert.equal(options.method, "POST");
    assert.equal(options.body, body);
    assert.equal(options.headers["Content-Type"], undefined);
    assert.equal(JSON.parse(options.body.get("options")).labels_verified, true);
    return { ok: true, text: async () => '{"id":"dataset-one"}' };
  });
  assert.deepEqual(await api("/datasets", { method: "POST", body }), { id: "dataset-one" });
});
