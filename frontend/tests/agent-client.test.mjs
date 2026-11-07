import test from "node:test";
import assert from "node:assert/strict";
import { createAPI, escapeHtml, candidateGraph, summarizeUsage, toolEvents, memoryPresentation } from "../src/agent-client.mjs";

test("tool trace joins journal outcomes by stable call identity", () => {
  const transcript = [
    { role: "assistant", tool_calls: [{ id: "a", function: { name: "write", arguments: '{"path":"graph.json"}' } }, { id: "b", function: { name: "bash", arguments: '{"command":"python test.py"}' } }] },
    { role: "tool", tool_call_id: "a", content: '{"output":"written","is_error":false}' },
  ];
  const events = toolEvents(transcript, [{ call_id: "b", name: "bash", status: "completed", result: { output: "assertion failed", is_error: true } }]);
  assert.equal(events[0].name, "write");
  assert.equal(events[0].result.output, "written");
  assert.equal(events[0].status, "completed");
  assert.equal(events[1].name, "bash");
  assert.equal(events[1].result.is_error, true);
  assert.equal(events[1].result.output, "assertion failed");
});

test("journal-only calls remain visible while transcript persistence catches up", () => {
  const events = toolEvents([], [{ call_id: "early", name: "bash", status: "running", result: null }]);
  assert.deepEqual(events.map(({ id, name, status }) => ({ id, name, status })), [{ id: "early", name: "bash", status: "running" }]);
});

test("candidate preview accepts root nodes with null needs and rejects arbitrary JSON", () => {
  assert.deepEqual(candidateGraph('{"name":"candidate","nodes":[{"id":"root","needs":null}]}').nodes[0].needs, []);
  assert.equal(candidateGraph('{"nodes":[null]}'), null);
  assert.equal(candidateGraph('{"nodes":[{"id":"node","needs":[{"toString":"invalid"}]}]}'), null);
  assert.equal(candidateGraph('{"nodes":[]}'), null);
  assert.equal(candidateGraph('not json'), null);
});

test("escape all untrusted text before template rendering", () => {
  assert.equal(escapeHtml('<img src=x onerror="alert(1)"> & \'x\''), "&lt;img src=x onerror=&quot;alert(1)&quot;&gt; &amp; &#39;x&#39;");
});

test("usage reflects provider counters including failed zero-token calls", () => {
  assert.deepEqual(summarizeUsage([{ input_tokens: 8, output_tokens: 3 }, { input_tokens: 0, output_tokens: 0 }]), { calls: 2, input: 8, output: 3 });
});

test("API uses same-origin JSON and retains a stable session ID on retries", async () => {
  const bodies = [];
  const request = createAPI(async (path, options) => {
    assert.equal(path, "/api/sessions");
    assert.equal(options.headers["Content-Type"], "application/json");
    bodies.push(JSON.parse(options.body));
    return { ok: true, text: async () => '{"id":"same-session","duplicate":true}' };
  });
  const options = { method: "POST", body: { id: "same-session", task: "scope", prompt: "build" } };
  await request("/sessions", options);
  await request("/sessions", options);
  assert.equal(bodies[0].id, bodies[1].id);
});

test("API surfaces unavailable health checks and rejects a static-server response", async () => {
  const health = createAPI(async () => ({ ok: false, status: 503, text: async () => '{"ready":false,"checks":{"worker":"unavailable"}}' }));
  await assert.rejects(health("/health"), (error) => error.status === 503 && error.data.checks.worker === "unavailable");
  const staticServer = createAPI(async () => ({ ok: true, text: async () => "<!doctype html>" }));
  await assert.rejects(staticServer("/sessions"), /make demo/);
});

test("local memory explains disabled Hindsight without claiming pending processing", () => {
  const local = memoryPresentation({ backend: "local", facts: [], warning: "Hindsight is disabled." }, true);
  assert.equal(local.warning, "Hindsight is disabled.");
  assert.match(local.empty, /source journal/);
  assert.doesNotMatch(local.empty, /asynchronous|processing/);
  const hindsight = memoryPresentation({ backend: "hindsight", facts: [] }, true);
  assert.equal(hindsight.warning, "");
  assert.match(hindsight.empty, /asynchronous/);
});
