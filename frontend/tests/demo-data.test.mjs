import test from "node:test";
import assert from "node:assert/strict";
import { createSyntheticDemoCSV } from "../src/demo-data.mjs";

test("synthetic demo produces reproducible labelled data and matching label-free serving inputs", () => {
  const demo = createSyntheticDemoCSV();
  assert.deepEqual(createSyntheticDemoCSV(), demo);
  assert.equal(demo.rowCount, 5000);
  assert.match(demo.labelMeaning, /not a real insurance fraud label/);
  const evaluation = demo.evaluationCSV.trim().split("\n");
  const inference = demo.inferenceCSV.trim().split("\n");
  assert.equal(evaluation.shift(), "signal,context,noise,label");
  assert.equal(inference.shift(), "signal,context,noise");
  assert.equal(evaluation.length, 5000);
  assert.equal(inference.length, evaluation.length);
  const counts = new Map();
  evaluation.forEach((row, index) => {
    const columns = row.split(","), label = columns.pop();
    assert.ok(label === "0" || label === "1");
    assert.equal(columns.length, 3);
    assert.ok(columns.every((value) => Number.isFinite(Number(value))));
    assert.equal(columns.join(","), inference[index]);
    counts.set(label, (counts.get(label) || 0) + 1);
  });
  assert.ok(counts.get("0") >= 10 && counts.get("1") >= 10);
});
