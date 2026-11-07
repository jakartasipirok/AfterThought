export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

export function displayStatus(status) {
  return String(status || "queued").toLowerCase().replace(/^workflow_execution_status_/, "").replaceAll("_", " ");
}

export function statusClass(status) {
  return displayStatus(status).replace(/[^a-z0-9]+/g, "-");
}

export function summarizeUsage(usage = []) {
  return usage.reduce((summary, call) => ({
    calls: summary.calls + 1,
    input: summary.input + (Number(call.input_tokens) || 0),
    output: summary.output + (Number(call.output_tokens) || 0),
  }), { calls: 0, input: 0, output: 0 });
}

export function parseJSON(value, fallback = null) {
  try { return JSON.parse(value); } catch { return fallback; }
}

// Join the transcript with the execution journal by call identity, not array
// position. A journal result can arrive before its transcript is persisted.
export function toolEvents(transcript = [], journal = []) {
  const calls = new Map();
  const results = new Map();
  for (const message of transcript) {
    for (const call of message.tool_calls || []) {
      calls.set(call.id, { id: call.id, name: call.function?.name || "tool", arguments: call.function?.arguments || "{}" });
    }
    if (message.role === "tool") results.set(message.tool_call_id, parseJSON(message.content, { output: message.content }));
  }
  for (const record of journal) {
    const call = calls.get(record.call_id) || { id: record.call_id, name: record.name || "tool", arguments: "{}" };
    calls.set(record.call_id, { ...call, status: record.status, result: record.result || results.get(record.call_id) });
  }
  return [...calls.values()].map((call) => ({ ...call, result: call.result || results.get(call.id), status: call.status || (results.has(call.id) ? "completed" : "pending") }));
}

export function candidateGraph(content) {
  const graph = parseJSON(content);
  if (!graph || typeof graph !== "object" || !Array.isArray(graph.nodes) || !graph.nodes.length || graph.nodes.length > 16) return null;
  if (graph.name != null && typeof graph.name !== "string" || graph.version != null && typeof graph.version !== "string") return null;
  if (!graph.nodes.every((node) => node && typeof node.id === "string" && (node.needs == null || Array.isArray(node.needs) && node.needs.every((dependency) => typeof dependency === "string")))) return null;
  return { ...graph, nodes: graph.nodes.map((node) => ({ ...node, needs: node.needs || [] })) };
}

export function memoryPresentation(data, recall = false) {
  const local = recall && data.backend === "local";
  return {
    items: (recall ? data.facts : data.records) || [],
    warning: data.warning || (local ? "Hindsight is disabled in explicit local memory mode." : ""),
    empty: local ? "Use the source journal to inspect local execution outcomes and human feedback." : recall ? "No recalled facts yet. Hindsight retention is asynchronous; try again after processing." : "No journal entries in this scope yet. Completed sessions and human feedback appear here.",
  };
}

export function createAPI(fetcher = globalThis.fetch) {
  return async function request(path, { method = "GET", body, signal, timeout = 15000 } = {}) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal?.aborted) abort();
    else signal?.addEventListener("abort", abort, { once: true });
    const timer = setTimeout(abort, timeout);
    try {
      const response = await fetcher(`/api${path}`, {
        method,
        signal: controller.signal,
        headers: body === undefined ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      const text = await response.text();
      const data = parseJSON(text);
      if (!response.ok) {
        const error = new Error(data?.error || `Agent API returned HTTP ${response.status}.`);
        error.status = response.status;
        error.data = data;
        throw error;
      }
      if (!data || typeof data !== "object") throw new Error("The server did not return agent data. Start the demo with make demo.");
      return data;
    } catch (error) {
      if (error.name === "AbortError") {
        if (signal?.aborted) throw error;
        throw new Error("The agent request timed out. You can retry safely using the same session ID.");
      }
      if (error instanceof TypeError) throw new Error("Could not reach the agent API. Check that make demo is running.");
      throw error;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", abort);
    }
  };
}
