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
