import { escapeHtml } from "./agent-client.mjs";
export { escapeHtml };

export function probability(value) {
  return typeof value === "number" && Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : "Not reported";
}

export function featureColumns(value) {
  return [...new Set(String(value).split(",").map((name) => name.trim()).filter(Boolean))];
}

// These are display comparisons against the frozen policy. A release's server
// status remains the authority for eligibility and human activation.
export function evaluationGates(report, policy = {}) {
  const metrics = report?.metrics || {};
  const frozen = report?.policy || policy;
  return [
    ["Precision", "precision", "min_precision", "≥", (actual, limit) => actual >= limit],
    ["Recall", "recall", "min_recall", "≥", (actual, limit) => actual >= limit],
    ["False positive rate", "false_positive_rate", "max_false_positive_rate", "≤", (actual, limit) => actual <= limit],
  ].map(([name, field, limitField, comparison, evaluate]) => {
    const actual = metrics[field], limit = frozen[limitField];
    const known = typeof actual === "number" && Number.isFinite(actual) && typeof limit === "number" && Number.isFinite(limit);
    return { name, actual, limit, comparison, status: known ? evaluate(actual, limit) ? "passed" : "failed" : "unknown" };
  });
}

export function servingCounters(run = {}) {
  const field = (key) => typeof run[key] === "number" && Number.isFinite(run[key]) ? run[key] : null;
  const calls = field("model_calls"), input = field("input_tokens"), output = field("output_tokens");
  return { calls, input, output, verifiedZero: calls === 0 && input === 0 && output === 0 };
}

// Never render arbitrary CSV/source columns returned alongside predictions.
export function predictionPreview(preview = []) {
  return preview.slice(0, 100).map((item, index) => {
    const value = item?.prediction && typeof item.prediction === "object" ? item.prediction : item || {};
    const score = value.score ?? value.probability ?? value.fraud_probability;
    const decision = value.recommended_decision ?? value.decision ?? value.recommendation;
    const reasons = value.reasons ?? value.reason;
    return {
      row: typeof value.row_id === "string" || typeof value.row_id === "number" ? value.row_id : Number.isInteger(item?.row_index) ? item.row_index : index + 1,
      score: typeof score === "number" ? score : null,
      decision: typeof decision === "string" ? decision : "Not reported",
      reasons: Array.isArray(reasons) ? reasons.filter((reason) => typeof reason === "string").join(" · ").slice(0, 2000) : typeof reasons === "string" ? reasons.slice(0, 2000) : "Not reported",
    };
  });
}

export function graphLayout(dag = {}) {
  const raw = Array.isArray(dag.nodes) ? dag.nodes.slice(0, 32) : [];
  const nodes = raw.map((node) => ({ ...node, needs: Array.isArray(node.needs) ? node.needs.filter((dep) => typeof dep === "string") : [] }));
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const levels = new Map(), visiting = new Set();
  let invalid = false;
  function level(node) {
    if (levels.has(node.id)) return levels.get(node.id);
    if (visiting.has(node.id)) { invalid = true; return 0; }
    visiting.add(node.id);
    const depth = node.needs.reduce((max, id) => {
      if (!byId.has(id)) { invalid = true; return max; }
      return Math.max(max, level(byId.get(id)) + 1);
    }, 0);
    visiting.delete(node.id); levels.set(node.id, depth); return depth;
  }
  nodes.forEach(level);
  const positions = new Map(), used = new Map();
  const placed = nodes.map((node) => {
    const depth = Math.min(levels.get(node.id) || 0, 31), row = used.get(depth) || 0;
    used.set(depth, row + 1);
    const position = { ...node, x: 25 + depth * 220, y: 22 + row * 105 };
    positions.set(node.id, position); return position;
  });
  const edges = placed.flatMap((node) => node.needs.filter((id) => positions.has(id)).map((id) => ({ from: positions.get(id), to: node })));
  return { nodes: placed, edges, width: Math.max(400, ...placed.map((node) => node.x + 190)), height: Math.max(118, ...placed.map((node) => node.y + 92)), invalid };
}

export function createGraphAPI(fetcher = globalThis.fetch) {
  return async function request(path, { method = "GET", body, timeout = 45000, signal } = {}) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal?.aborted) abort(); else signal?.addEventListener("abort", abort, { once: true });
    const timer = setTimeout(abort, timeout);
    const multipart = typeof FormData !== "undefined" && body instanceof FormData;
    try {
      const response = await fetcher(`/api${path}`, { method, signal: controller.signal, headers: multipart || body === undefined ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" }, ...(body === undefined ? {} : { body: multipart ? body : JSON.stringify(body) }) });
      const text = await response.text();
      let data; try { data = JSON.parse(text); } catch { throw new Error("Graph API did not return JSON. Run the current demo server with make demo."); }
      if (!response.ok) { const error = new Error(data.error || `Graph API returned HTTP ${response.status}.`); error.status = response.status; error.data = data; throw error; }
      return data;
    } catch (error) {
      if (error.name === "AbortError" && !signal?.aborted) throw new Error("The request timed out. Retry keeps the same job ID when work may already have started.");
      if (error instanceof TypeError) throw new Error("Could not reach the graph API. Check that the current demo server is running.");
      throw error;
    } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
  };
}
