// Deterministic preparation of operator-supplied CSVs. No model or graph code.
export function parseCSV(text) {
  const records = [];
  let row = [], cell = "", quoted = false;
  text = text.replace(/^\uFEFF/, "").replace(/\r\n/g, "\n");
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') {
      if (quoted && text[i + 1] === '"') { cell += '"'; i++; }
      else if (!quoted && cell) throw new Error("Unexpected quote in CSV.");
      else quoted = !quoted;
    } else if (!quoted && (c === "," || c === "\n")) {
      row.push(cell); cell = "";
      if (c === "\n") { if (row.some(value => value !== "")) records.push(row); row = []; }
    } else cell += c;
  }
  if (quoted) throw new Error("Unclosed quote in CSV.");
  if (cell || row.length) { row.push(cell); records.push(row); }
  const headers = records.shift();
  if (!headers?.length || new Set(headers).size !== headers.length) throw new Error("CSV needs unique headers.");
  return records.map(values => {
    if (values.length !== headers.length) throw new Error("CSV row has the wrong number of columns.");
    return Object.fromEntries(headers.map((name, i) => [name, values[i]]));
  });
}

function shuffle(rows, seed) {
  rows = [...rows];
  for (let i = rows.length - 1; i > 0; i--) {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
    const j = seed % (i + 1);
    [rows[i], rows[j]] = [rows[j], rows[i]];
  }
  return rows;
}

export function prepareLearningData(originalCSV, newCSV, novelCSV) {
  const sources = [originalCSV, newCSV, novelCSV].map(parseCSV);
  const target = "FraudFound_P";
  const excluded = new Set([target, "PolicyNumber", "RepNumber", "AddressChange_Claim"]);
  const features = Object.keys(sources[0][0] || {}).filter(key => !excluded.has(key));
  if (!features.length || !sources[0][0]?.[target]) throw new Error("Expected the fraud_oracle schema and FraudFound_P target.");
  const groups = new Map();
  let duplicates = 0, conflicts = 0;
  sources.forEach((rows, source) => rows.forEach(row => {
    if (!["0", "1"].includes(row[target]) || features.some(key => !(key in row))) throw new Error("All files must share features and binary FraudFound_P labels.");
    const key = JSON.stringify(features.map(name => row[name]));
    const previous = groups.get(key);
    if (previous) {
      duplicates++;
      if (previous.row[target] !== row[target] && !previous.conflict) { previous.conflict = true; conflicts++; }
    } else groups.set(key, { row, source, conflict: false });
  }));
  const clean = [[], [], []];
  for (const entry of groups.values()) if (!entry.conflict) clean[entry.source].push(entry.row);
  const positives = shuffle(clean[0].filter(row => row[target] === "1"), 7319);
  const negatives = shuffle(clean[0].filter(row => row[target] === "0"), 1147);
  const additions = clean.slice(1).map((rows, i) => shuffle(rows.filter(row => row[target] === "1"), 2801 + i));
  if (positives.length < 700 || negatives.length < 1200 || additions.some(rows => rows.length < 60)) throw new Error("This prepared experiment needs 700 baseline fraud cases, 1,200 legitimate cases and at least 60 fraud cases in each challenge CSV.");
  const takeNegative = count => negatives.splice(0, count);
  const reserve = [positives.splice(0, 200), additions[0].splice(0, 50), additions[1].splice(0, 30)];
  const benchmark = reserve.flat();
  benchmark.push(...takeNegative(benchmark.length));
  const baseline = positives.splice(0, 200);
  baseline.push(...takeNegative(baseline.length));
  const roundOne = positives.splice(0, 300);
  roundOne.push(...takeNegative(roundOne.length));
  const rounds = [roundOne, ...additions.map(rows => [...rows, ...takeNegative(rows.length)])];
  const columns = [...features, target];
  const encode = rows => columns.join(",") + "\n" + shuffle(rows, 937).map(row => columns.map(key => {
    const value = row[key];
    return /[,"\n\r]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
  }).join(",")).join("\n") + "\n";
  return {
    version: 1, target, featureColumns: features, positiveLabel: "1", synthetic: false,
    labelMeaning: "Operator-supplied FraudFound_P labels: 1 indicates a fraud-labeled claim and 0 a legitimate-labeled claim. Labels and challenge provenance have not been independently adjudicated.",
    sourceCounts: sources.map(rows => rows.length), duplicates, conflicts,
    sampling: "Balanced demonstration sample. The original CSV contains approximately 6% fraud. Scores here describe the balanced benchmark. Challenge labels are supplied by the operator.",
    baseline: { name: "Initial historical claims", rows: baseline.length, csv: encode(baseline) },
    benchmark: { name: "Fixed mixed-pattern benchmark", rows: benchmark.length, csv: encode(benchmark) },
    rounds: rounds.map((rows, i) => ({ name: ["More historical examples", "New fraud examples", "Novel-pattern examples"][i], rows: rows.length, csv: encode(rows) })),
  };
}
