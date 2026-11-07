import { activityMarkup } from "./authoring-trace.mjs";
import { createAPI, escapeHtml as esc, displayStatus, statusClass, summarizeUsage, toolEvents, candidateGraph, memoryPresentation } from "./agent-client.mjs";

const $ = (selector) => document.querySelector(selector);
const api = createAPI();
const number = (value) => Number(value || 0).toLocaleString();
const state = { sessions: [], selected: new URLSearchParams(location.search).get("session"), detail: null, task: null, health: null, pendingStart: null, busy: false, polling: false, detailKey: null, detailRequest: 0, selection: null, file: null, memoryMode: "journal", memoryVersion: 0, drafts: new Map() };

function badge(status) { return `<span class="badge ${statusClass(status)}"><span class="badge-dot"></span>${esc(displayStatus(status))}</span>`; }
function errorMessage(error) { return error?.message || "An unexpected request error occurred."; }
function showError(message = "") { $("#globalError").textContent = message; $("#globalError").hidden = !message; }
function formStatus(selector, message, error = false) { $(selector).textContent = message; $(selector).classList.toggle("error", error); }
function formatDate(value) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? "" : date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); }
function sessionTask(session) { return session?.task || session?.task_id || ""; }
function currentStatus() { return state.detail?.execution_status || state.detail?.session?.status || "queued"; }

function renderMetrics() {
  const usage = summarizeUsage(state.detail?.usage);
  const cards = [
    ["Recent sessions", number(state.sessions.length), "Latest journal records", "▦"],
    ["Model calls", number(usage.calls), state.health?.model || "Selected session", "✦"],
    ["Tokens used", number(usage.input + usage.output), `${number(usage.input)} input · ${number(usage.output)} output`, "↗"],
    ["Tool executions", number((state.detail?.tools || []).length), "Selected session · recorded evidence", "⌘"],
  ];
  $("#agentMetrics").innerHTML = cards.map(([label, value, detail, icon]) => `<article class="metric-card"><div class="metric-top"><span>${label}</span><span class="metric-icon">${icon}</span></div><strong>${value}</strong><small>${esc(detail)}</small></article>`).join("");
}

function renderSessions() {
  $("#sessionCount").textContent = `${state.sessions.length} recent`;
  $("#sessionList").innerHTML = state.sessions.map((session) => `<button type="button" class="session-card ${session.id === state.selected ? "selected" : ""}" data-session="${esc(session.id)}" aria-pressed="${session.id === state.selected}"><strong title="${esc(session.id)}">${esc(session.id)}</strong><div class="session-card-footer">${badge(session.id === state.selected && state.detail ? currentStatus() : session.status)}<time>${esc(formatDate(session.created_at))}</time></div><small>${esc(sessionTask(session))}</small></button>`).join("") || '<p class="empty-state">No sessions yet. Describe a task above to start with an empty workspace.</p>';
}

function renderFilePreview() {
  const host = $("#filePreview");
  if (!host || !state.file || state.file.session !== state.selected) return;
  const file = state.file;
  if (file.loading || file.error) { host.innerHTML = `<div class="pending-note">${esc(file.error || `Loading ${file.path}…`)}</div>`; return; }
  const graph = candidateGraph(file.content);
  const graphMarkup = graph ? `<div class="graph-preview" aria-label="Agent-authored graph candidate">${graph.nodes.map((node, index) => `${index ? '<span class="graph-arrow">→</span>' : ""}<div class="graph-node"><strong>${esc(node.id)}</strong><small>Needs: ${esc(node.needs.join(", ") || "$input")}</small><small>${node.id === graph.output ? "Graph output" : "Candidate node"}</small></div>`).join("")}</div><p class="graph-caption">Candidate: ${esc(graph.name)} · ${esc(graph.version)}. Nodes are shown in manifest order; dependencies are listed on each card. This preview does not validate or execute the graph.</p>` : "";
  host.innerHTML = `<div class="file-preview"><div class="file-preview-head"><strong>${esc(file.path)}</strong><button type="button" class="text-button" id="downloadFile">Download visible text ↓</button></div>${graphMarkup}<pre>${esc(file.content || "(empty file)")}</pre>${file.truncated ? '<p class="graph-caption">File preview is truncated. Use the CLI workspace for the complete artifact.</p>' : ""}</div>`;
  $("#downloadFile").onclick = () => {
    const link = document.createElement("a");
    const url = URL.createObjectURL(new Blob([file.content], { type: "text/plain;charset=utf-8" }));
    link.href = url; link.download = file.path.split("/").pop() || "artifact.txt"; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
}

function renderDetail() {
  const detail = state.detail;
  if (!detail) return;
  const openCalls = new Set([...document.querySelectorAll(".trace-event[open]")].map((element) => element.dataset.trace || element.dataset.call));
  const scroll = $(".trace-list")?.scrollTop || 0;
  const session = detail.session || { id: state.selected, task: state.task };
  const status = currentStatus();
  const usage = summarizeUsage(detail.usage);
  const transcript = detail.transcript || [];
  const events = toolEvents(transcript, detail.tools);
  const toolErrors = events.filter((event) => event.result?.is_error).length;
  const assistant = [...transcript].reverse().find((message) => message.role === "assistant" && message.content);
  const failed = /failed|timed.out|terminated|cancelled|canceled|budget/i.test(status);
  const result = failed ? session.result || detail.progress?.text || assistant?.content : detail.progress?.text || assistant?.content || session.result;
  const prompt = [...transcript].reverse().find((message) => message.role === "user");
  const memories = transcript.filter((message) => message.role === "user" && /^(Prior task memory|Hindsight recalled learning evidence)/.test(message.content));
  $("#detailStatus").innerHTML = badge(status);
  $("#sessionDetail").innerHTML = `<div class="session-meta"><code>${esc(session.id)}</code><span>Scope: ${esc(sessionTask(session) || state.task)}</span><button type="button" class="text-button" data-reuse-scope>Use this scope for next session ↗</button><a href="http://127.0.0.1:8233" target="_blank" rel="noopener noreferrer">Temporal dashboard ↗</a></div>${detail.warning ? `<div class="detail-warning">${esc(detail.warning)}</div>` : ""}<div class="run-summary"><div><span>Model turns recorded</span><strong>${number(detail.progress?.turns || usage.calls)}</strong></div><div><span>Tool outcomes</span><strong>${number(events.filter((event) => event.status === "completed").length)} / ${number(events.length)} returned${toolErrors ? ` · ${number(toolErrors)} error${toolErrors === 1 ? "" : "s"}` : ""}</strong></div><div><span>Files authored</span><strong>${number((detail.files || []).length)}</strong></div></div>${prompt ? `<details class="trace-event input-evidence"><summary>Task input</summary><pre>${esc(prompt.content)}</pre></details>` : ""}${memories.length ? `<details class="trace-event input-evidence"><summary>Learning context recalled before authoring · ${memories.length} source${memories.length === 1 ? "" : "s"}</summary>${memories.map((message) => `<pre>${esc(message.content)}</pre>`).join("")}</details>` : ""}${failed ? '<div class="detail-warning">This session ended without a successful authoring completion. Retained files and tool outputs remain available for review.</div>' : ""}<section class="detail-section"><h3>${/completed/i.test(status) ? "Agent result" : "Latest visible response"}</h3>${result ? `<p class="result-text">${esc(result)}</p>` : '<p class="pending-note">Waiting for the next model or tool result. Sessions continue durably when this page is closed.</p>'}</section><section class="detail-section"><h3>Model responses and tool activity</h3>${activityMarkup(detail)}</section><section class="detail-section"><h3>Workspace files <span class="muted">· generated by this session</span></h3><div class="files-list">${(detail.files || []).map((file) => `<button type="button" class="file-link ${state.file?.path === file.path ? "selected" : ""}" data-file="${esc(file.path)}">${esc(file.path)}<small>${number(file.size)} B</small></button>`).join("") || '<p class="pending-note">Files appear here after the agent writes them.</p>'}</div><div id="filePreview"></div></section>`;
  document.querySelectorAll(".trace-event").forEach((element) => { element.open = openCalls.has(element.dataset.trace || element.dataset.call); });
  if ($(".trace-list")) $(".trace-list").scrollTop = scroll;
  renderFilePreview();
}

function setTask(task) {
  if (task === state.task) return;
  if (state.task) state.drafts.set(state.task, $("#feedbackInput").value);
  state.task = task;
  $("#feedbackInput").value = state.drafts.get(task) || "";
  $("#memoryScope").textContent = task ? `Learning scope: ${task}` : "Select a session to inspect its learning scope.";
  $("#feedbackButton").disabled = !task;
  $("#loadMemoryButton").disabled = !task;
  formStatus("#feedbackStatus", "");
  state.memoryVersion++;
  if (task) loadMemory();
}

async function selectSession(id, task) {
  state.selection?.abort();
  state.selection = new AbortController();
  state.selected = id; state.detail = null; state.detailKey = null; state.file = null;
  if (task) setTask(task);
  const url = new URL(location.href); url.searchParams.set("session", id); history.replaceState({}, "", url);
  $("#sessionDetail").innerHTML = '<p class="empty-state">Loading execution evidence…</p>';
  $("#detailStatus").innerHTML = badge("queued"); renderSessions(); renderMetrics();
  await loadDetail();
}

async function loadDetail() {
  if (!state.selected) return;
  const id = state.selected;
  const version = ++state.detailRequest;
  const signal = state.selection?.signal;
  try {
    const detail = await api(`/sessions/${encodeURIComponent(id)}`, { signal });
    if (state.selected !== id || signal?.aborted || version !== state.detailRequest) return;
    state.detail = detail;
    setTask(sessionTask(detail.session) || state.task);
    const key = JSON.stringify(detail);
    if (key !== state.detailKey) { state.detailKey = key; renderDetail(); renderMetrics(); renderSessions(); }
  } catch (error) {
    if (error.name === "AbortError" || state.selected !== id || version !== state.detailRequest) return;
    if (!state.detail) $("#sessionDetail").innerHTML = `<p class="empty-state">${esc(errorMessage(error))} This session may still be initializing; the next refresh will check again.</p>`;
    else showError(errorMessage(error));
  }
}

async function openFile(path) {
  const id = state.selected;
  state.file = { session: id, path, loading: true }; renderFilePreview();
  try {
    const file = await api(`/sessions/${encodeURIComponent(id)}/files?${new URLSearchParams({ path })}`, { signal: state.selection?.signal });
    if (state.selected !== id || state.file?.path !== path) return;
    state.file = { ...file, path, session: id }; renderFilePreview();
    document.querySelectorAll("[data-file]").forEach((button) => button.classList.toggle("selected", button.dataset.file === path));
  } catch (error) {
    if (error.name === "AbortError" || state.selected !== id || state.file?.path !== path) return;
    state.file = { session: id, path, error: errorMessage(error) }; renderFilePreview();
  }
}

function renderMemory(data, recall) {
  const { items, warning, empty } = memoryPresentation(data, recall);
  $("#memoryResults").innerHTML = (warning ? `<div class="detail-warning">${esc(warning)}</div>` : "") + (items.length ? items.map((item) => {
    const source = item.source || item.metadata?.source || item.provenance || "Unattributed; verify journal";
    const derived = item.type === "observation" || item.provenance?.startsWith("derived");
    const sources = item.sources || [];
    return `<article class="memory-entry"><span class="source-label ${derived ? "derived" : source.startsWith("human:") ? "human" : ""}">${esc(derived ? `Derived observation · ${source}` : source)}</span><p>${esc(item.content || item.text)}</p>${derived ? '<p class="pending-note">Verify this pattern against source facts; it is not ground truth.</p>' : ""}${sources.length ? `<details><summary>${sources.length} supporting source fact${sources.length === 1 ? "" : "s"}</summary>${sources.map((fact) => `<pre>${esc(fact.metadata?.source || "Unattributed")}: ${esc(fact.text)}</pre>`).join("")}</details>` : ""}</article>`;
  }).join("") : `<p class="empty-state">${esc(empty)}</p>`);
}

async function loadMemory() {
  if (!state.task) return;
  const task = state.task, recall = state.memoryMode === "recall", version = ++state.memoryVersion;
  if (recall && state.health?.memory_backend === "local") {
    renderMemory({ backend: "local", facts: [] }, true);
    $("#recallButton").disabled = true;
    return;
  }
  $("#memoryResults").innerHTML = `<p class="empty-state">${recall ? "Recalling attributed learning evidence…" : "Loading source journal…"}</p>`;
  $("#recallButton").disabled = true;
  try {
    const data = await api(`/tasks/${encodeURIComponent(task)}/${recall ? "recall" : "memory"}`, recall ? { method: "POST", body: { query: $("#recallQuery").value.trim() }, timeout: 60000 } : {});
    if (state.task !== task || version !== state.memoryVersion) return;
    renderMemory(data, recall);
  } catch (error) {
    if (state.task === task && version === state.memoryVersion) $("#memoryResults").innerHTML = `<p class="empty-state">${esc(errorMessage(error))}</p>`;
  } finally { if (version === state.memoryVersion) $("#recallButton").disabled = false; }
}

async function refresh() {
  if (state.polling) return;
  state.polling = true;
  try {
    const [health, sessions] = await Promise.allSettled([api("/health"), api("/sessions")]);
    const healthData = health.status === "fulfilled" ? health.value : health.reason?.data;
    if (healthData?.checks) {
      state.health = healthData;
      $("#recallTab").textContent = healthData.memory_backend === "local" ? "Hindsight (disabled)" : "Hindsight recall";
      if (state.memoryMode === "recall" && healthData.memory_backend === "local") { $("#recallButton").disabled = true; renderMemory({ backend: "local", facts: [] }, true); }
      const ready = healthData.ready === true;
      $("#connectionPill").textContent = ready ? "● Agent ready" : "● Services need attention";
      $("#connectionPill").classList.toggle("offline", !ready); $("#serviceDot").classList.toggle("offline", !ready);
      $("#serviceLabel").textContent = ready ? "Authoring harness ready" : "Check local services";
      $("#startButton").disabled = state.busy || !ready;
      if (!ready) showError(`The authoring harness is not ready. Service checks: ${Object.entries(healthData.checks || {}).map(([name, value]) => `${name}: ${value}`).join(" · ")}. Start dependencies with make up, then make demo.`);
      else showError();
    } else {
      $("#connectionPill").textContent = "● API unavailable"; $("#connectionPill").classList.add("offline"); $("#serviceDot").classList.add("offline"); $("#serviceLabel").textContent = "API unavailable";
      $("#startButton").disabled = true; showError(errorMessage(health.reason));
    }
    if (sessions.status === "fulfilled") {
      state.sessions = sessions.value.sessions || []; renderSessions();
      if (!state.selected && state.sessions.length) await selectSession(state.sessions[0].id, sessionTask(state.sessions[0]));
    } else if (!state.sessions.length) $("#sessionList").innerHTML = `<p class="empty-state">${esc(errorMessage(sessions.reason))}</p>`;
    await loadDetail(); renderMetrics();
  } finally { state.polling = false; }
}

$("#sessionForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (state.busy) return;
  const values = { task: $("#taskInput").value.trim(), prompt: $("#promptInput").value.trim(), dataset: $("#datasetInput").value.trim(), max_turns: Number($("#turnsInput").value) };
  if (!values.prompt && !values.dataset) { formStatus("#startStatus", "Add an objective or a dataset URL before starting.", true); $("#promptInput").focus(); return; }
  if (!values.task) return;
  const key = JSON.stringify(values);
  if (state.pendingStart?.key !== key) state.pendingStart = { key, payload: { ...values, id: `web-${crypto.randomUUID()}` } };
  const payload = state.pendingStart.payload;
  state.busy = true; $("#startButton").disabled = true; $("#startButton").textContent = "Starting…"; formStatus("#startStatus", `Submitting ${payload.id}…`);
  try {
    const response = await api("/sessions", { method: "POST", body: payload });
    state.pendingStart = null;
    formStatus("#startStatus", `${response.duplicate ? "Reconnected to" : "Started"} ${response.id || payload.id}. Follow the execution below.`);
    await selectSession(response.id || payload.id, response.task || payload.task);
    await refresh();
    $("#sessionPanel").scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth", block: "nearest" });
  } catch (error) {
    if (error.status && error.status < 500) state.pendingStart = null;
    formStatus("#startStatus", `${errorMessage(error)}${state.pendingStart ? " Retry keeps the same session ID to avoid duplicate work." : ""}`, true);
  } finally { state.busy = false; $("#startButton").disabled = state.health?.ready !== true; $("#startButton").textContent = state.pendingStart ? "Retry submission →" : "Start agent →"; }
});

$("#exampleButton").onclick = () => {
  $("#datasetInput").value = "";
  $("#promptInput").value = "Read the graph-workflow skill. Author a small graph.proposal.json candidate and a Python node for checking an incoming claim record. Use synthetic sample inputs only; write a reproducible test and run it with bash. Leave unresolved artifact/image references explicit. Explain which checks were tested and which would require real data or human policy decisions. Do not claim fraud accuracy or a promoted production graph.";
  $("#promptInput").focus();
};
$("#refreshButton").onclick = refresh;
$("#sessionList").onclick = (event) => { const button = event.target.closest("[data-session]"); if (button) selectSession(button.dataset.session, sessionTask(state.sessions.find((session) => session.id === button.dataset.session))); };
$("#sessionDetail").onclick = (event) => {
  if (event.target.closest("[data-reuse-scope]") && state.task) {
    $("#taskInput").value = state.task;
    formStatus("#startStatus", `Learning scope set to ${state.task}. Your task draft is unchanged; describe the next iteration before starting.`);
    $("#taskInput").focus();
    $("#sessionForm").scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth", block: "center" });
    return;
  }
  const button = event.target.closest("[data-file]");
  if (button) openFile(button.dataset.file);
};
$("#loadMemoryButton").onclick = loadMemory;
$("#journalTab").onclick = () => { state.memoryMode = "journal"; $("#journalTab").classList.add("selected"); $("#recallTab").classList.remove("selected"); $("#recallForm").hidden = true; loadMemory(); };
$("#recallTab").onclick = () => { state.memoryMode = "recall"; $("#recallTab").classList.add("selected"); $("#journalTab").classList.remove("selected"); $("#recallForm").hidden = false; loadMemory(); };
$("#recallForm").onsubmit = (event) => { event.preventDefault(); loadMemory(); };
$("#feedbackForm").onsubmit = async (event) => {
  event.preventDefault();
  const task = state.task;
  const message = $("#feedbackInput").value.trim(), actor = $("#actorInput").value.trim();
  if (!task || !message || !actor) return;
  $("#feedbackButton").disabled = true; formStatus("#feedbackStatus", "Saving grounded feedback…");
  try {
    const response = await api(`/tasks/${encodeURIComponent(task)}/feedback`, { method: "POST", body: { actor, message }, timeout: 60000 });
    state.drafts.delete(task);
    if (state.task !== task) return;
    $("#feedbackInput").value = "";
    formStatus("#feedbackStatus", response.warning || (response.memory_delivery === "local" ? "Saved to the source journal. Local memory mode is active." : response.memory_delivery === "pending" ? "Saved to the source journal. Hindsight delivery is pending." : "Saved to the source journal and accepted for asynchronous Hindsight processing."));
    await loadMemory();
  } catch (error) { if (state.task === task) formStatus("#feedbackStatus", errorMessage(error), true); }
  finally { $("#feedbackButton").disabled = !state.task; }
};

renderMetrics();
if (state.selected) state.selection = new AbortController();
refresh().then(() => {
  // Session evidence changes the page height after the browser's initial
  // fragment scroll. Re-align only if memory is still the requested location.
  if (location.hash === "#learning") $("#learning")?.scrollIntoView({ block: "start" });
});
setInterval(() => { if (!document.hidden) refresh(); }, 3000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
