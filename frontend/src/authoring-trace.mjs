import { escapeHtml as esc, summarizeUsage, toolEvents, displayStatus } from "./agent-client.mjs";

export function activityMarkup(detail = {}) {
  const usage = summarizeUsage(detail.usage), transcript = detail.transcript || [];
  const events = new Map(toolEvents(transcript, detail.tools).map(event => [event.id, event]));
  const rendered = new Set();
  function tool(event) {
    rendered.add(event.id);
    return `<details class="trace-event" data-trace="tool-${esc(event.id)}"><summary><strong>${esc(event.name)}</strong><span>${esc(event.result?.is_error ? "error" : event.status)}</span></summary><div class="trace-label">Request</div><pre>${esc(event.arguments)}</pre><div class="trace-label">Response${event.result?.truncated ? " · truncated by harness" : ""}</div><pre>${esc(event.result?.output ?? "Waiting for tool result…")}</pre></details>`;
  }
  const rows = transcript.flatMap((message, index) => {
    if (message.role !== "assistant") return [];
    const parts = message.content ? [`<details class="trace-event" data-trace="message-${index}"><summary><strong>Model response</strong><span>${esc(message.content.slice(0, 100))}</span></summary><pre>${esc(message.content)}</pre></details>`] : [];
    for (const call of message.tool_calls || []) if (events.has(call.id)) parts.push(tool(events.get(call.id)));
    return parts;
  });
  for (const event of events.values()) if (!rendered.has(event.id)) rows.push(tool(event));
  return `<div class="run-summary"><div><span>Model calls</span><strong>${usage.calls}</strong></div><div><span>Input tokens</span><strong>${usage.input.toLocaleString()}</strong></div><div><span>Output tokens</span><strong>${usage.output.toLocaleString()}</strong></div></div><p class="form-hint">${esc(displayStatus(detail.execution_status || detail.session?.status))} · Recorded responses and tool results; updates after each call.</p><div class="trace-list">${rows.join("") || '<p class="pending-note">Waiting for the first recorded response…</p>'}</div>`;
}

export function renderActivity(host, detail) {
  const markup = activityMarkup(detail);
  if (host._activityMarkup === markup) return;
  const open = new Set([...host.querySelectorAll("[data-trace][open]")].map(node => node.dataset.trace));
  const scroll = host.querySelector(".trace-list")?.scrollTop || 0;
  host.innerHTML = markup; host._activityMarkup = markup;
  for (const node of host.querySelectorAll("[data-trace]")) node.open = open.has(node.dataset.trace);
  host.querySelector(".trace-list").scrollTop = scroll;
}

export function activityLoader(api, host) {
  let selected = null, version = 0;
  return async function load(id) {
    const request = ++version;
    if (id !== selected) { selected = id; host._activityMarkup = null; host.textContent = id ? "Loading agent activity…" : "Activity appears when authoring starts."; }
    if (!id) return;
    try {
      const detail = await api(`/sessions/${encodeURIComponent(id)}`);
      if (request === version && id === selected) renderActivity(host, detail);
    } catch (error) { if (request === version) { host._activityMarkup = null; host.textContent = error.status === 404 ? "Waiting for the authoring session to start…" : error.message; } }
  };
}
