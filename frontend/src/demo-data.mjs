// Synthetic classification data for exercising the graph lifecycle. This does
// not seed graph nodes or represent insurance outcomes.
export function createSyntheticDemoCSV() {
  const featureColumns = ["signal", "context", "noise"];
  const evaluation = [...featureColumns, "label"].join(",") + "\n";
  const inference = featureColumns.join(",") + "\n";
  const labelledRows = [], featureRows = [];
  let seed = 7319;
  const random = () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
    return seed / 4294967296;
  };
  for (let index = 0; index < 5000; index++) {
    const values = Array.from({ length: 3 }, () => random().toFixed(6));
    const [signal, context] = values.map(Number);
    const label = 2 * signal + context - 1.5 + (random() - 0.5) * 0.08 >= 0 ? 1 : 0;
    const features = values.join(",");
    featureRows.push(features);
    labelledRows.push(`${features},${label}`);
  }
  return {
    evaluationCSV: evaluation + labelledRows.join("\n") + "\n",
    inferenceCSV: inference + featureRows.join("\n") + "\n",
    rowCount: labelledRows.length,
    featureColumns,
    target: "label",
    positiveLabel: "1",
    labelMeaning: "Generated positive class for testing graph mechanics; not a real insurance fraud label.",
  };
}

// Fictional motor claims with a deliberately novel evidence-reuse signal.
// No real fraud labels or image analysis are implied.
export function createPolicyDemoCSV() {
  const featureColumns = ["claim_amount", "repair_estimate_ratio", "evidence_reuse_count"];
  const labelled = [], ordinary = [], emerging = [];
  for (let i = 0; i < 1000; i++) {
    const amount = 500 + ((i * 7919) % 19500);
    const ratio = (0.5 + ((i * 37) % 200) / 100).toFixed(2);
    const reuse = i % 3;
    const row = `${amount},${ratio},${reuse}`;
    ordinary.push(row);
    labelled.push(`${row},${Number(ratio) >= 1.5 ? 1 : 0}`);
    // Keep the established model signal unchanged; shift a previously quiet field.
    emerging.push(`${amount},${ratio},${8 + i % 5}`);
  }
  const header = featureColumns.join(",");
  return { featureColumns, target: "synthetic_label", positiveLabel: "1", rowCount: 1000,
    labelMeaning: "Synthetic demonstration rule: repair_estimate_ratio >= 1.5. These are generated labels, not verified insurance fraud outcomes.",
    evaluationCSV: `${header},synthetic_label\n${labelled.join("\n")}\n`,
    inferenceCSV: `${header}\n${ordinary.join("\n")}\n`,
    emergingCSV: `${header}\n${emerging.join("\n")}\n` };
}
