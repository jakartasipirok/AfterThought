import { createGraphAPI, escapeHtml as esc, featureColumns, probability, evaluationGates, servingCounters, predictionPreview, graphLayout } from "./graph-client.mjs";
import { displayStatus, statusClass } from "./agent-client.mjs";

const $ = (selector) => document.querySelector(selector);
const api = createGraphAPI();
const params = new URLSearchParams(location.search);
const state = {
  task: params.get("task") || "insurance-fraud-demo", datasets: [], dataset: null, datasetID: params.get("dataset"),
  jobs: [], jobID: params.get("job"), job: null, releases: [], releaseID: params.get("release"), release: null, releaseKey: "", activeID: "",
  node: null, nodeSource: null, sourceRequest: 0, releaseRequest: 0, tuneTarget: null, approvalID: null, approvalActiveSnapshot: null, seenJobRelease: null,
  runID: params.get("run"), run: null, preview: [], health: null, polling: false, loading: false,
  pendingBootstrap: null, pendingTune: null, pendingBatch: null, submitting: new Set(),
};
$("#graphTask").value = state.task;
const num = (value) => typeof value === "number" ? value.toLocaleString() : "—";
const pretty = (value) => JSON.stringify(value, null, 2);
const date = (value) => { const stamp = new Date(value); return Number.isNaN(stamp.valueOf()) ? "" : stamp.toLocaleString(); };
const badge = (value, label) => `<span class="badge ${statusClass(value)}"><span class="badge-dot"></span>${esc(label || displayStatus(value))}</span>`;
const errorText = (error) => error?.message || "The request failed.";
function status(selector, message = "", failed = false) { $(selector).textContent = message; $(selector).classList.toggle("error", failed); }
function showError(message = "") { $("#graphError").textContent = message; $("#graphError").hidden = !message; }
function updateURL() {
  const url = new URL(location.href);
  for (const [key, value] of Object.entries({ task: state.task, dataset: state.datasetID, job: state.jobID, release: state.releaseID, run: state.runID })) {
    if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
  }
  history.replaceState({}, "", url);
}

function renderMetrics() {
  const counters = servingCounters(state.run || {});
  const cards = [
    ["Evaluation rows", num(state.dataset?.rows), state.dataset?.synthetic ? "Synthetic dataset · demo only" : "Selected frozen dataset", "▦"],
    ["Selected candidate", state.release ? displayStatus(state.release.status) : "—", state.release?.id || "No release selected", "◇"],
    ["Active release", state.activeID ? "Approved" : "None", state.activeID || "Human approval required", "✓"],
    ["Last batch model calls", num(counters.calls), counters.verifiedZero ? "0 input tokens · 0 output tokens" : state.run ? `${num(counters.input)} input · ${num(counters.output)} output tokens` : "No serving run observed", "↗"],
  ];
  $("#graphMetrics").innerHTML = cards.map(([name, value, detail, icon]) => `<article class="metric-card"><div class="metric-top"><span>${name}</span><span class="metric-icon">${icon}</span></div><strong>${esc(value)}</strong><small title="${esc(detail)}">${esc(detail)}</small></article>`).join("");
}

function datasetID(record) { return record?.id || ""; }
function renderDatasets() {
  $("#datasetSelect").innerHTML = '<option value="">Choose a dataset or upload below</option>' + state.datasets.map((item) => `<option value="${esc(item.id)}" ${item.id === state.datasetID ? "selected" : ""}>${esc(item.name || item.id)} · ${num(item.rows)} rows${item.synthetic ? " · synthetic" : ""}</option>`).join("");
  const item = state.dataset;
  if (!item) { $("#datasetSummary").textContent = "No dataset selected."; updateControls(); return; }
  const profile = item.profile || {}, policy = item.policy || {};
  const splits = profile.split_counts || {};
  $("#datasetSummary").innerHTML = `<div class="dataset-summary-title"><strong>${esc(item.name || item.id)}</strong>${badge(item.synthetic ? "synthetic" : "verified", item.synthetic ? "Synthetic evidence" : "Verified labels")}</div><code>${esc(item.id)}</code><div class="dataset-facts"><span>${num(item.rows)} rows</span><span>Target: ${esc(item.target)}</span><span>Positive: ${esc(item.positive_label)}</span><span>${(item.feature_columns || []).length} permitted features</span></div><p>${esc(item.label_meaning)}</p><div class="split-grid">${["train", "development", "audit"].map((split) => `<div><span>${split}</span><strong>${num(splits[split]?.rows)}</strong><small>${num(splits[split]?.positive)} positive · ${num(splits[split]?.negative)} negative</small></div>`).join("")}</div><div class="frozen-policy"><strong>Frozen policy</strong><span>Precision ≥ ${probability(policy.min_precision)}</span><span>Recall ≥ ${probability(policy.min_recall)}</span><span>FPR ≤ ${probability(policy.max_false_positive_rate)}</span><span>Threshold ${probability(policy.threshold)}</span></div><details class="profile-details"><summary>Feature profile · names and missing counts only</summary><div class="profile-fields">${(profile.fields || []).map((field) => `<span><strong>${esc(field.name)}</strong><small>${esc(field.type)} · ${num(field.missing)} missing</small></span>`).join("") || `<p>${esc((item.feature_columns || []).join(", "))}</p>`}</div></details>`;
  updateControls();
}

async function chooseDataset(id) {
  state.datasetID = id || null; state.dataset = null; renderDatasets(); renderMetrics(); updateURL();
  if (!id) return;
  try {
    const data = await api(`/datasets/${encodeURIComponent(id)}`);
    if (state.datasetID !== id) return;
    state.dataset = data.dataset || data;
    renderDatasets(); renderMetrics();
  } catch (error) { if (state.datasetID === id) showError(errorText(error)); }
}

function renderJobs() {
  const jobs = state.job && !state.jobs.some((item) => item.id === state.job.id) ? [state.job, ...state.jobs] : state.jobs;
  $("#bootstrapJobs").innerHTML = jobs.length ? jobs.map((job) => `<article class="bootstrap-job ${job.id === state.jobID ? "selected" : ""}"><div><button type="button" class="text-button job-name" data-job="${esc(job.id)}">${esc(job.id)}</button><div class="job-meta"><span>Phase: ${esc(job.phase || "queued")}</span>${job.node_id ? `<span>Node revision: ${esc(job.node_id)}</span>` : ""}${job.session_id ? `<a href="./?${new URLSearchParams({ session: job.session_id })}">Authoring session ↗</a>` : ""}${job.release_id ? `<button type="button" class="text-button" data-release="${esc(job.release_id)}">Inspect release ↗</button>` : ""}</div>${job.error ? `<p class="job-error">${esc(job.error)}</p>` : ""}</div>${badge(job.status || "queued")}</article>`).join("") : '<p class="empty-state">No bootstrap jobs in this scope. Start with a frozen dataset and an objective.</p>';
}

function renderReleaseList() {
  $("#releaseCount").textContent = `${state.releases.length}`;
  $("#releaseList").innerHTML = state.releases.map((item) => `<button type="button" class="session-card ${item.id === state.releaseID ? "selected" : ""}" data-release="${esc(item.id)}"><strong>${esc(item.graph?.name || item.id)}</strong><div class="session-card-footer">${badge(item.id === state.activeID ? "active" : item.status, item.id === state.activeID ? "Active · human approved" : undefined)}</div><small>${esc(item.id)}</small>${item.parent_release_id ? `<small>Revision of ${esc(item.parent_release_id)}</small>` : ""}</button>`).join("") || '<p class="empty-state">No evaluated releases yet.</p>';
  const selected = $("#batchRelease").value;
  const approved = state.releases.filter((release) => release.id === state.activeID || release.status === "approved" || release.approved === true || release.approved_at);
  $("#batchRelease").innerHTML = approved.length ? approved.map((release) => `<option value="${esc(release.id)}" ${release.id === (selected || state.activeID) ? "selected" : ""}>${esc(release.id)}${release.id === state.activeID ? " · active" : " · approved"}</option>`).join("") : '<option value="">No human approved release available</option>';
  updateControls();
}

function dagMarkup(dag, phase) {
  const layout = graphLayout(dag);
  if (!layout.nodes.length) return `<p class="empty-state">No ${phase} nodes reported.</p>`;
  return `<div class="dag-scroll"><svg class="dag-svg" width="${layout.width}" height="${layout.height}" viewBox="0 0 ${layout.width} ${layout.height}" aria-label="${phase} graph nodes and dependency edges"><defs><marker id="${phase}-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8" fill="#a4bcdd"/></marker></defs>${layout.edges.map(({ from, to }) => `<path d="M${from.x + 170},${from.y + 33} C${from.x + 193},${from.y + 33} ${to.x - 23},${to.y + 33} ${to.x},${to.y + 33}" fill="none" stroke="#a4bcdd" stroke-width="1.5" marker-end="url(#${phase}-arrow)"/>`).join("")}${layout.nodes.map((node) => `<g class="dag-node ${state.node?.phase === phase && state.node?.id === node.id ? "selected" : ""}" role="button" tabindex="0" data-node="${esc(node.id)}" data-phase="${phase}" aria-label="Inspect ${esc(phase)} node ${esc(node.id)}"><title>${esc(node.id)} · depends on ${esc(node.needs.join(", ") || "$input")}</title><rect x="${node.x}" y="${node.y}" width="170" height="66" rx="9"/><text x="${node.x + 13}" y="${node.y + 27}" class="dag-node-name">${esc(node.id.length > 23 ? `${node.id.slice(0, 21)}…` : node.id)}</text><text x="${node.x + 13}" y="${node.y + 47}" class="dag-node-note">${node.id === dag.output ? "Graph output" : node.needs.length ? `${node.needs.length} dependencies` : "Input node"}</text></g>`).join("")}</svg></div>${layout.invalid ? '<p class="detail-warning">The declared dependency graph contains missing dependencies or a cycle. Review the source contract.</p>' : ""}`;
}

function reportMarkup(report, title, policy) {
  if (!report || !report.dataset_id || !report.split) return `<article class="evaluation-report"><h3>${title}</h3><p class="pending-note">No evaluation report has been recorded for this split.</p></article>`;
  const metrics = report.metrics || {};
  return `<article class="evaluation-report"><div class="evaluation-title"><h3>${title}</h3>${badge(report.passed ? "passed" : "failed", report.passed ? "Policy passed" : "Policy failed")}</div>${report.synthetic ? '<p class="synthetic-note">Synthetic data · mechanics evidence only</p>' : ""}<div class="evaluation-samples">${num(metrics.total)} evaluated rows · ${report.labels_verified ? "labels verified" : "labels not verified"}</div><table class="evaluation-table"><thead><tr><th>Metric</th><th>Observed</th><th>Frozen gate</th><th>Result</th></tr></thead><tbody>${evaluationGates(report, policy).map((gate) => `<tr><td>${gate.name}</td><td>${probability(gate.actual)}</td><td>${gate.comparison} ${probability(gate.limit)}</td><td>${badge(gate.status, gate.status === "unknown" ? "Not reported" : undefined)}</td></tr>`).join("")}</tbody></table><div class="evaluation-extras"><span>Accuracy <strong>${probability(metrics.accuracy)}</strong></span><span>F1 <strong>${probability(metrics.f1)}</strong></span><span>Majority baseline <strong>${probability(metrics.majority_baseline)}</strong></span></div><div class="confusion-counts"><span>TP <strong>${num(metrics.true_positive)}</strong></span><span>TN <strong>${num(metrics.true_negative)}</strong></span><span>FP <strong>${num(metrics.false_positive)}</strong></span><span>FN <strong>${num(metrics.false_negative)}</strong></span></div>${(report.failures || []).length ? `<ul class="gate-failures">${report.failures.map((failure) => `<li>${esc(failure)}</li>`).join("")}</ul>` : ""}</article>`;
}

function renderRelease() {
  const release = state.release;
  if (!release) return;
  const tuneDraft = $("#tunePrompt")?.value || "";
  const graph = release.graph || {}, policy = release.audit?.policy || release.development?.policy || state.dataset?.policy || {};
  $("#releaseHeading").textContent = graph.name || "Fit and serve graphs";
  $("#releaseStatus").innerHTML = badge(release.id === state.activeID ? "active" : release.status, release.id === state.activeID ? "Human approved · active" : undefined);
  $("#releaseDetail").innerHTML = `<div class="release-meta"><code>${esc(release.id)}</code><span>Dataset ${esc(release.dataset_id)}</span>${release.parent_release_id ? `<span>Parent ${esc(release.parent_release_id)}</span>` : ""}${release.node_id ? `<span>Changed node ${esc(release.node_id)}</span>` : ""}<span>${esc(date(release.created_at))}</span></div>${release.execution_error ? `<div class="detail-warning"><strong>Graph execution failed.</strong> ${esc(release.execution_error)} The retained graph and source remain available for inspection and one-node revision.</div>` : ""}<section class="dag-phase"><div class="dag-heading"><h3>Fit graph</h3><span>Frozen training data → fitted artifact</span></div>${dagMarkup(graph.fit || {}, "fit")}</section><section class="dag-phase"><div class="dag-heading"><h3>Serve graph</h3><span>Batch inputs + fitted artifact → recommendations</span></div>${dagMarkup(graph.serve || {}, "serve")}</section><div id="nodeInspector" class="node-inspector"><p class="empty-state">Select a node to inspect pinned source code and configuration.</p></div><section class="evaluation-section"><h3 class="detail-section-heading">Measured evaluation evidence</h3><div class="evaluation-grid">${reportMarkup(release.development, "Development split", policy)}${reportMarkup(release.audit, "Reserved audit split", policy)}</div><p class="audit-reuse-note">Candidates reuse the same frozen split. Audit labels are withheld from the author, while operators can inspect these metrics. Repeated review requires a fresh audit dataset for a final independent accuracy claim.</p></section><section class="tune-panel"><div><h3>Improve exactly one node</h3><p>Choose a node above, then prepare a revision. The server preserves every other node’s source, configuration, and topology.</p></div><form id="tuneForm"><div id="tuneTarget" class="tune-target">No node selected for revision.</div><label for="tunePrompt">Grounded improvement request<textarea id="tunePrompt" rows="3" maxlength="12000" placeholder="Describe the observed failure, evidence, and desired change to this node." required></textarea></label><div class="form-actions"><span class="form-hint">The new candidate must pass evaluation and receive separate human approval.</span><button class="primary-button" id="startTune" disabled>Author one-node revision →</button></div><div id="tuneStatus" class="form-status" role="status"></div></form></section>`;
  $("#tuneForm").onsubmit = submitTune;
  $("#tunePrompt").value = tuneDraft;
  if (state.tuneTarget?.release_id === release.id) $("#tuneTarget").textContent = `Revision target: ${state.tuneTarget.node_id} in ${release.id}`;
  if (state.node) renderNodeInspector();
  syncApproval(); renderMetrics();
}

function setRelease(release) {
  if (!release?.id) return;
  const changed = state.releaseID !== release.id;
  if (changed) { state.node = null; state.nodeSource = null; state.tuneTarget = null; state.sourceRequest++; }
  state.releaseID = release.id; state.release = release;
  const key = JSON.stringify(release);
  if (key !== state.releaseKey || changed) { state.releaseKey = key; renderRelease(); }
  syncApproval(); renderReleaseList(); renderMetrics(); updateURL();
}

async function chooseRelease(id) {
  if (!id) return;
  const request = ++state.releaseRequest, task = state.task;
  try {
    const data = await api(`/releases/${encodeURIComponent(id)}`);
    if (request !== state.releaseRequest || task !== state.task) return;
    setRelease(data.release || data);
  } catch (error) { showError(errorText(error)); }
}

function renderNodeInspector() {
  const host = $("#nodeInspector");
  if (!host || !state.node) return;
  const key = `${state.node.phase}:${state.node.id}`;
  const data = state.nodeSource;
  host.innerHTML = `<div class="node-inspector-heading"><div><span class="eyebrow">PINNED NODE</span><h3>${esc(key)}</h3></div><button type="button" class="secondary-button" id="prepareTune" ${data?.loading ? "disabled" : ""}>Prepare revision for this node ↗</button></div>${data?.loading ? '<p class="pending-note">Loading immutable source and configuration…</p>' : data?.error ? `<p class="detail-warning">${esc(data.error)}</p>` : `<details open><summary>Source code</summary><pre class="source-code">${esc(data?.source || "No source text returned.")}</pre></details><details><summary>Node configuration and contract</summary><pre class="source-code">${esc(pretty(data?.node || {}))}</pre></details>`}`;
  $("#prepareTune").onclick = () => {
    state.tuneTarget = { release_id: state.releaseID, node_id: key };
    $("#tuneTarget").textContent = `Revision target: ${key} in ${state.releaseID}`;
    $("#startTune").disabled = !state.health?.ready;
    $("#tunePrompt").focus();
  };
}

async function inspectNode(phase, id) {
  if (!state.release || !["fit", "serve"].includes(phase)) return;
  state.node = { phase, id }; state.nodeSource = { loading: true };
  const releaseID = state.releaseID, request = ++state.sourceRequest;
  document.querySelectorAll(".dag-node").forEach((node) => node.classList.toggle("selected", node.dataset.phase === phase && node.dataset.node === id));
  renderNodeInspector();
  try {
    const data = await api(`/releases/${encodeURIComponent(releaseID)}/nodes/${phase}/${encodeURIComponent(id)}`);
    if (request !== state.sourceRequest || releaseID !== state.releaseID) return;
    state.nodeSource = data; renderNodeInspector();
  } catch (error) { if (request === state.sourceRequest) { state.nodeSource = { error: errorText(error) }; renderNodeInspector(); } }
}

function syncApproval() {
  const release = state.release, eligible = release?.status === "eligible", active = release?.id === state.activeID;
  const approvalID = eligible && !active ? release.id : null;
  if (state.approvalID !== approvalID || state.approvalActiveSnapshot !== state.activeID) {
    state.approvalID = approvalID; state.approvalActiveSnapshot = state.activeID;
    $("#approvalCode").value = ""; $("#approvalConfirmed").checked = false; status("#approvalStatus");
  }
  $("#approvalTarget").innerHTML = release ? `<strong>${esc(release.id)}</strong>${badge(active ? "active" : release.status, active ? "Already active" : eligible ? "Eligible · awaiting human decision" : undefined)}<small>Dataset: ${esc(release.dataset_id)} · Current active release: ${esc(state.activeID || "none")}</small>` : "Select an eligible candidate to review.";
  for (const selector of ["#approverActor", "#approvalCode", "#approvalConfirmed", "#approveReleaseButton"]) $(selector).disabled = !approvalID || state.submitting.has("approval");
}

function updateControls() {
  $("#startBootstrap").disabled = !state.datasetID || !state.health?.ready || state.submitting.has("bootstrap");
  $("#startBatch").disabled = !$("#batchRelease").value || !state.health?.ready || state.submitting.has("batch");
  if ($("#startTune")) $("#startTune").disabled = !state.tuneTarget || !state.health?.ready || state.submitting.has("tune");
  $("#uploadDatasetButton").disabled = state.submitting.has("dataset");
  syncApproval();
}

async function loadJob() {
  if (!state.jobID) return;
  const id = state.jobID;
  try {
    const data = await api(`/bootstraps/${encodeURIComponent(id)}`);
    if (id !== state.jobID) return;
    state.job = data.job || data; renderJobs();
    const releaseID = data.release?.id || state.job.release_id;
    const newRelease = releaseID && state.seenJobRelease !== `${id}/${releaseID}`;
    if (newRelease) state.seenJobRelease = `${id}/${releaseID}`;
    if (data.release && (newRelease || state.releaseID === releaseID)) setRelease(data.release);
    else if (newRelease) await chooseRelease(releaseID);
  } catch (error) { if (id === state.jobID) status("#bootstrapStatus", errorText(error), true); }
}

function renderBatch() {
  const run = state.run;
  if (!run) return;
  const counters = servingCounters(run), preview = predictionPreview(state.preview);
  $("#batchResult").innerHTML = `<div class="batch-result-head"><div><code>${esc(run.id || state.runID)}</code><small>Release ${esc(run.release_id)} · ${num(run.row_count)} rows</small></div>${badge(run.status || "queued")}${run.status === "completed" ? `<a class="secondary-button" href="/api/graph-runs/${encodeURIComponent(run.id || state.runID)}/output" download>Download complete output ↓</a>` : ""}</div><div class="batch-counters"><div><span>Observed model calls</span><strong>${num(counters.calls)}</strong></div><div><span>Input tokens</span><strong>${num(counters.input)}</strong></div><div><span>Output tokens</span><strong>${num(counters.output)}</strong></div><div>${counters.verifiedZero ? badge("passed", "Zero LLM calls verified") : badge("unknown", "Counters as reported")}</div></div>${run.error ? `<p class="detail-warning">${esc(run.error)}</p>` : ""}${(run.trace || []).length ? `<details class="batch-trace"><summary>Node execution trace · ${run.trace.length} entries</summary><pre class="source-code">${esc(pretty(run.trace))}</pre></details>` : ""}${preview.length ? `<div class="prediction-caption">Showing ${preview.length} returned predictions, limited to 100. These are model recommendations for human review.</div><div class="table-scroll"><table class="prediction-table"><thead><tr><th>Row</th><th>Model score</th><th>Recommended decision</th><th>Reasons</th></tr></thead><tbody>${preview.map((row) => `<tr><td>${esc(row.row)}</td><td>${probability(row.score)}</td><td>${esc(row.decision)}</td><td>${esc(row.reasons)}</td></tr>`).join("")}</tbody></table></div>` : '<p class="empty-state">No prediction preview returned yet. The completed batch output remains available for download.</p>'}`;
  renderMetrics();
}

async function loadRun() {
  if (!state.runID) return;
  const id = state.runID;
  try { const data = await api(`/graph-runs/${encodeURIComponent(id)}`); if (id === state.runID) { state.run = data.run || data; state.preview = data.preview || []; renderBatch(); } }
  catch (error) { if (id === state.runID) status("#batchStatus", errorText(error), true); }
}

async function refresh() {
  if (state.polling) return;
  state.polling = true;
  const scope = state.task;
  try {
    const query = new URLSearchParams({ task: scope });
    const results = await Promise.allSettled([api("/health"), api(`/datasets?${query}`), api(`/bootstraps?${query}`), api(`/releases?${query}`)]);
    if (scope !== state.task) return;
    const [health, datasets, jobs, releases] = results;
    const healthData = health.status === "fulfilled" ? health.value : health.reason?.data;
    state.health = healthData || null;
    const ready = healthData?.ready === true;
    $("#graphConnection").textContent = ready ? "● Runtime connected" : "● Services unavailable"; $("#graphConnection").classList.toggle("offline", !ready);
    $("#graphServiceLabel").textContent = ready ? "Local runtime ready" : "Check local services"; $("#graphServiceDot").classList.toggle("offline", !ready);
    const errors = results.slice(1).filter((item) => item.status === "rejected");
    if (errors.length) showError(errorText(errors[0].reason)); else if (!ready) showError(health.status === "rejected" ? errorText(health.reason) : "A database, Temporal, or worker check is unavailable. Start dependencies with make up, then make demo."); else showError();
    if (datasets.status === "fulfilled") {
      state.datasets = datasets.value.datasets || (Array.isArray(datasets.value) ? datasets.value : []);
      renderDatasets();
      if (state.datasetID && !state.dataset) await chooseDataset(state.datasetID);
    }
    if (jobs.status === "fulfilled") { state.jobs = jobs.value.jobs || []; renderJobs(); }
    if (releases.status === "fulfilled") {
      state.releases = releases.value.releases || []; state.activeID = releases.value.active_release_id || "";
      renderReleaseList();
      if (!state.releaseID && state.releases.length) await chooseRelease(state.releases[0].id);
      else if (state.releaseID) {
        const listed = state.releases.find((release) => release.id === state.releaseID);
        if (listed?.graph) setRelease(listed); else await chooseRelease(state.releaseID);
      }
    }
    await Promise.allSettled([loadJob(), loadRun()]);
    renderMetrics(); updateControls();
  } finally { state.polling = false; }
}

$("#scopeForm").onsubmit = (event) => {
  event.preventDefault();
  const task = $("#graphTask").value.trim(); if (!task || task === state.task) { refresh(); return; }
  state.task = task; state.datasets = []; state.dataset = null; state.datasetID = null; state.jobs = []; state.jobID = null; state.job = null;
  state.releases = []; state.release = null; state.releaseID = null; state.releaseKey = ""; state.activeID = ""; state.node = null; state.nodeSource = null; state.sourceRequest++;
  state.tuneTarget = null; state.run = null; state.runID = null; state.preview = []; state.pendingBootstrap = null; state.pendingTune = null; state.pendingBatch = null; state.releaseRequest++; state.seenJobRelease = null;
  $("#releaseDetail").innerHTML = '<p class="empty-state">Select a release in this scope.</p>'; $("#releaseStatus").innerHTML = ""; $("#releaseHeading").textContent = "Fit and serve graphs";
  $("#batchResult").innerHTML = '<p class="empty-state">No batch selected in this scope.</p>'; $("#scopeDescription").textContent = `Loaded scope: ${task}`;
  updateURL(); renderDatasets(); renderJobs(); renderReleaseList(); renderMetrics(); refresh();
};

$("#datasetForm").onsubmit = async (event) => {
  event.preventDefault(); if (state.submitting.has("dataset")) return;
  const file = $("#datasetFile").files[0]; if (!file) return;
  const options = { name: $("#datasetName").value.trim(), task: state.task, target: $("#targetColumn").value.trim(), positive_label: $("#positiveLabel").value.trim(), label_meaning: $("#labelMeaning").value.trim(), feature_columns: featureColumns($("#featureColumns").value), labels_verified: $("#labelsVerified").checked, synthetic: $("#syntheticDataset").checked, min_precision: Number($("#minPrecision").value), min_recall: Number($("#minRecall").value), max_false_positive_rate: Number($("#maxFPR").value), threshold: Number($("#scoreThreshold").value) };
  if (!options.feature_columns.length) { status("#datasetStatus", "Declare at least one permitted feature column.", true); return; }
  const body = new FormData(); body.append("file", file); body.append("options", JSON.stringify(options));
  state.submitting.add("dataset"); updateControls(); status("#datasetStatus", "Uploading, validating labels, and freezing evaluation splits…");
  try {
    const response = await api("/datasets", { method: "POST", body, timeout: 90000 });
    const dataset = response.dataset || response;
    if (options.task !== state.task) return;
    state.dataset = dataset; state.datasetID = dataset.id;
    status("#datasetStatus", `Dataset frozen: ${dataset.id}. Evaluation policy and splits are fixed.`); $("#uploadDetails").open = false;
    updateURL(); await refresh(); renderDatasets(); renderMetrics();
  } catch (error) { status("#datasetStatus", errorText(error), true); }
  finally { state.submitting.delete("dataset"); updateControls(); }
};

$("#datasetSelect").onchange = (event) => chooseDataset(event.target.value);
$("#bootstrapForm").onsubmit = async (event) => {
  event.preventDefault(); if (state.submitting.has("bootstrap") || !state.datasetID) return;
  const values = { dataset_id: state.datasetID, task: state.task, prompt: $("#bootstrapPrompt").value.trim(), max_turns: Number($("#bootstrapTurns").value) };
  const key = JSON.stringify(values);
  if (state.pendingBootstrap?.key !== key) state.pendingBootstrap = { key, payload: { ...values, id: `bootstrap-${crypto.randomUUID()}` } };
  const payload = state.pendingBootstrap.payload;
  state.submitting.add("bootstrap"); updateControls(); status("#bootstrapStatus", `Submitting ${payload.id}…`);
  try {
    const data = await api("/bootstraps", { method: "POST", body: payload });
    if (payload.task !== state.task) return;
    state.jobID = data.id || payload.id; state.pendingBootstrap = null; updateURL();
    status("#bootstrapStatus", `Bootstrap ${state.jobID} started. No release will be activated automatically.`); await refresh();
  } catch (error) { if (error.status && error.status < 500) state.pendingBootstrap = null; status("#bootstrapStatus", `${errorText(error)}${state.pendingBootstrap ? " Retry reuses the same job ID." : ""}`, true); }
  finally { state.submitting.delete("bootstrap"); updateControls(); }
};

async function submitTune(event) {
  event.preventDefault(); if (!state.tuneTarget || state.submitting.has("tune")) return;
  const values = { node_id: state.tuneTarget.node_id, prompt: $("#tunePrompt").value.trim(), max_turns: 20 };
  const releaseID = state.tuneTarget.release_id, task = state.task, key = JSON.stringify({ releaseID, ...values });
  if (state.pendingTune?.key !== key) state.pendingTune = { key, payload: { ...values, id: `tune-${crypto.randomUUID()}` } };
  const payload = state.pendingTune.payload;
  state.submitting.add("tune"); updateControls(); status("#tuneStatus", `Submitting revision of ${values.node_id}…`);
  try {
    const data = await api(`/releases/${encodeURIComponent(releaseID)}/tune`, { method: "POST", body: payload });
    if (task !== state.task) return;
    state.jobID = data.id || payload.id; state.pendingTune = null; updateURL(); status("#tuneStatus", `Revision ${state.jobID} started. All other nodes must remain unchanged.`); await refresh();
  } catch (error) { if (error.status && error.status < 500) state.pendingTune = null; if ($("#tuneStatus")) status("#tuneStatus", `${errorText(error)}${state.pendingTune ? " Retry keeps the same job ID." : ""}`, true); }
  finally { state.submitting.delete("tune"); updateControls(); }
}

$("#approvalForm").onsubmit = async (event) => {
  event.preventDefault();
  const releaseID = state.approvalID;
  if (!releaseID || !$("#approvalConfirmed").checked || state.submitting.has("approval")) return;
  const actor = $("#approverActor").value.trim(), approval_code = $("#approvalCode").value;
  if (!actor || !approval_code) return;
  const expected_current_id = state.approvalActiveSnapshot || "", task = state.task;
  $("#approvalCode").value = "";
  state.submitting.add("approval"); updateControls(); status("#approvalStatus", `Submitting your explicit approval of ${releaseID}…`);
  try {
    const data = await api(`/releases/${encodeURIComponent(releaseID)}/approve`, { method: "POST", body: { actor, approved: true, approval_code, expected_current_id } });
    if (task !== state.task) return;
    state.activeID = data.active_release_id || releaseID; $("#approvalConfirmed").checked = false;
    status("#approvalStatus", `Human approval recorded. Active release: ${state.activeID}.`); await refresh();
  } catch (error) { $("#approvalConfirmed").checked = false; status("#approvalStatus", `${errorText(error)} Re-enter the host code and explicitly confirm before retrying.`, true); }
  finally { state.submitting.delete("approval"); updateControls(); }
};

$("#batchForm").onsubmit = async (event) => {
  event.preventDefault(); if (state.submitting.has("batch")) return;
  const file = $("#batchFile").files[0], releaseID = $("#batchRelease").value, task = state.task;
  if (!file || !releaseID) return;
  const key = JSON.stringify({ releaseID, name: file.name, size: file.size, modified: file.lastModified });
  if (state.pendingBatch?.key !== key) state.pendingBatch = { key, id: `batch-${crypto.randomUUID()}` };
  const id = state.pendingBatch.id, body = new FormData(); body.append("file", file); body.append("options", JSON.stringify({ id }));
  state.submitting.add("batch"); updateControls(); status("#batchStatus", `Uploading batch ${id}…`);
  try {
    const data = await api(`/releases/${encodeURIComponent(releaseID)}/runs`, { method: "POST", body, timeout: 90000 });
    if (task !== state.task) return;
    state.runID = data.id || id; state.run = null; state.preview = []; state.pendingBatch = null; updateURL();
    status("#batchStatus", `Batch ${state.runID} started using the approved graph.`); await loadRun();
  } catch (error) { if (error.status && error.status < 500) state.pendingBatch = null; status("#batchStatus", `${errorText(error)}${state.pendingBatch ? " Retry keeps the same run ID." : ""}`, true); }
  finally { state.submitting.delete("batch"); updateControls(); }
};

$("#refreshGraphs").onclick = refresh;
$("#bootstrapJobs").onclick = (event) => {
  const release = event.target.closest("[data-release]"); if (release) { chooseRelease(release.dataset.release); return; }
  const job = event.target.closest("[data-job]"); if (job) { state.jobID = job.dataset.job; updateURL(); loadJob(); }
};
$("#releaseList").onclick = (event) => { const button = event.target.closest("[data-release]"); if (button) chooseRelease(button.dataset.release); };
$("#releaseDetail").onclick = (event) => { const node = event.target.closest("[data-node]"); if (node) inspectNode(node.dataset.phase, node.dataset.node); };
$("#releaseDetail").onkeydown = (event) => { const node = event.target.closest("[data-node]"); if (node && ["Enter", " "].includes(event.key)) { event.preventDefault(); inspectNode(node.dataset.phase, node.dataset.node); } };
$("#batchRelease").onchange = updateControls;
renderMetrics(); updateControls(); refresh();
setInterval(() => { if (!document.hidden) refresh(); }, 3500);
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
