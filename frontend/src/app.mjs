import { evaluateClaim, reconcile, RULESET } from "./engine.mjs";

const $ = (selector) => document.querySelector(selector);
const formatMoney = (value) => `$${Number(value).toLocaleString("en-US")}`;
const state = { claims: [], refs: null, selected: null, filter: "All", query: "", sort: "recent", page: 0, decisions: {}, outage: null };
const PAGE_SIZE = 12;
const STORAGE_KEY = "afterthought-demo-investigations-v1";

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

function evaluation(claim) {
  return evaluateClaim(claim, state.refs, { referenceOutage: state.outage === claim.id });
}

function badge(label) {
  return `<span class="badge ${label.toLowerCase().replaceAll(" ", "-")}"><span class="badge-dot"></span>${escapeHtml(label)}</span>`;
}

function renderMetrics() {
  const counts = { Continue: 0, Review: 0, Hold: 0 };
  state.claims.forEach((claim) => counts[evaluation(claim).outcome]++);
  const cards = [
    ["Claims screened", state.claims.length.toLocaleString(), "Motor source records", "total", "▦"],
    ["Continue", counts.Continue.toLocaleString(), "Ready for next stage", "continue", "↗"],
    ["Review", counts.Review.toLocaleString(), "Human follow-up needed", "review", "◎"],
    ["Hold", counts.Hold.toLocaleString(), "Blocked prerequisites", "hold", "Ⅱ"],
  ];
  $("#metrics").innerHTML = cards.map(([label, number, detail, tone, icon]) => `<article class="metric-card ${tone}"><div class="metric-top"><span>${label}</span><span class="metric-icon">${icon}</span></div><strong>${number}</strong><small>${detail}</small></article>`).join("");
}

function renderFlow() {
  const steps = [
    ["01", "Screening", "Validate fields, dates, and references", "shield"],
    ["02", "Business checking", "Apply approved demo rules", "check"],
    ["03", "Dynamic routing", "Continue, Review, or Hold", "route"],
    ["04", "Investigation", "Resolve the flagged exception", "search"],
    ["05", "Reconciliation", "Confirm the next action", "layers"],
  ];
  $("#flow").innerHTML = steps.map(([number, name, detail], index) => `<div class="flow-step"><div class="flow-circle">${number}</div><strong>${name}</strong><small>${detail}</small>${index < steps.length - 1 ? '<span class="flow-arrow">→</span>' : ""}</div>`).join("");
}

function filteredClaims() {
  const query = state.query.toLowerCase();
  const claims = state.claims.filter((claim) => {
    const outcome = evaluation(claim).outcome;
    return (state.filter === "All" || state.filter === outcome) &&
      (!query || [claim.id, claim.agent_id, claim.vendor_id].some((part) => part.toLowerCase().includes(query)));
  });
  claims.sort((a, b) => state.sort === "amount" ? b.amount - a.amount || a.id.localeCompare(b.id) : b.received_at.localeCompare(a.received_at) || b.id.localeCompare(a.id));
  return claims;
}

function renderQueue() {
  const claims = filteredClaims();
  const maxPage = Math.max(0, Math.ceil(claims.length / PAGE_SIZE) - 1);
  state.page = Math.min(state.page, maxPage);
  const visible = claims.slice(state.page * PAGE_SIZE, (state.page + 1) * PAGE_SIZE);
  $("#recordCount").textContent = `${claims.length.toLocaleString()} claims`;
  $("#claimRows").innerHTML = visible.map((claim) => {
    const result = evaluation(claim);
    return `<tr data-id="${escapeHtml(claim.id)}"><td><button type="button" class="claim-id claim-link" aria-label="Open claim ${escapeHtml(claim.id)}">${escapeHtml(claim.id)}</button><small>${escapeHtml(claim.agent_id)}</small></td><td>${escapeHtml(claim.loss_date)}</td><td class="amount">${formatMoney(claim.amount)}</td><td>${escapeHtml(claim.severity)}</td><td>${badge(result.outcome)}</td><td>${escapeHtml(result.route)}</td><td class="row-arrow">↗</td></tr>`;
  }).join("") || '<tr><td colspan="7" class="empty">No claims match these filters.</td></tr>';
  $("#pageSummary").textContent = claims.length ? `Showing ${state.page * PAGE_SIZE + 1}–${Math.min((state.page + 1) * PAGE_SIZE, claims.length)} of ${claims.length.toLocaleString()}` : "Showing 0 claims";
  $("#prevPage").disabled = state.page === 0;
  $("#nextPage").disabled = state.page === maxPage;
}

function fact(label, value) { return `<div class="fact"><span>${label}</span><strong>${escapeHtml(value)}</strong></div>`; }
function stage(number, title, status, detail, body = "") {
  return `<section class="detail-stage"><div class="stage-number">${number}</div><div class="stage-content"><div class="stage-title"><strong>${title}</strong>${badge(status)}</div><p>${detail}</p>${body}</div></section>`;
}
function findings(items, emptyText) {
  return items.length ? `<div class="findings">${items.map((item) => `<div class="finding"><strong>${escapeHtml(item.text)}</strong><small>${escapeHtml(item.evidence)} · ${escapeHtml(item.code)}</small></div>`).join("")}</div>` : `<div class="pass-note">✓ ${emptyText}</div>`;
}

function renderDrawer() {
  if (!state.selected) return;
  const claim = state.claims.find((item) => item.id === state.selected);
  if (!claim) return;
  const focusedControl = $("#drawer").contains(document.activeElement) ? document.activeElement : null;
  const result = evaluation(claim);
  const investigation = state.decisions[claim.id];
  const reconciled = reconcile(result, investigation);
  const investigationStatus = result.outcome === "Hold" ? "Blocked" : result.outcome === "Continue" ? "Skipped" : investigation ? "Complete" : "Pending";
  const investigationDetail = result.outcome === "Hold" ? "Screening must pass before investigation." : result.outcome === "Continue" ? "No referral was created for this claim." : investigation ? `Recorded ${new Date(investigation.at).toLocaleString()}: ${investigation.note || investigation.decision}` : "Claims reviewer disposition required before reconciliation.";
  const actions = result.outcome === "Review" ? `<div class="investigation-actions"><label for="investigationNote">Investigation note</label><textarea id="investigationNote" placeholder="Record the evidence behind your decision">${escapeHtml(investigation?.note || "")}</textarea><div class="action-buttons"><button data-decision="clear" class="action-clear">Clear concern</button><button data-decision="request" class="action-request">Request evidence</button><button data-decision="escalate" class="action-escalate">Escalate concern</button></div><small>Demo actions are stored in this browser. They do not establish a fraud label.</small></div>` : "";
  const outageButton = `<button id="outageButton" class="text-button">${state.outage === claim.id ? "Restore reference service" : "Simulate reference outage"}</button>`;
  $("#drawer").innerHTML = `<div class="drawer-head"><div><span class="eyebrow">CLAIM WORKSPACE</span><h2>${escapeHtml(claim.id)}</h2><p>${badge(result.outcome)} <span class="muted">${escapeHtml(RULESET)}</span></p></div><button id="closeDrawer" class="close-button" aria-label="Close details">×</button></div><div class="drawer-scroll"><div class="detail-card"><div class="detail-card-title">Claim facts <span>Source record</span></div><div class="facts">${fact("Claim amount", formatMoney(claim.amount))}${fact("Loss date", claim.loss_date)}${fact("Report date", claim.report_date)}${fact("Severity", claim.severity)}${fact("Injury", claim.any_injury ? "Yes" : "No")}${fact("Police contacted", claim.authority_contacted === "Police" ? "Yes" : "No")}${fact("Police report", claim.police_report_available ? "Available" : "Missing")}${fact("Agent / vendor", `${claim.agent_id} / ${claim.vendor_id || "Not assigned"}`)}</div></div><div class="detail-heading">PROCESS TRACE <span>5 stages</span></div><div class="stage-list">${stage("01", "Screening", result.screening.length ? "Hold" : "Passed", "Required fields, chronological dates, and known references.", findings(result.screening, "Required fields and references verified."))}${stage("02", "Automated business checking", result.outcome === "Hold" ? "Blocked" : result.business.length ? "Flagged" : "Passed", "Fictional document, amount, and severity rules.", result.outcome === "Hold" ? "" : findings(result.business, "No business review flags."))}${stage("03", "Dynamic routing", result.outcome, `Assigned to ${result.route}.`)}${stage("04", "Investigation", investigationStatus, investigationDetail, actions)}${stage("05", "Reconciliation", reconciled.label, reconciled.detail)}</div><div class="simulator"><strong>Failure simulation</strong><p>Temporarily fail this claim’s agent reference check to inspect Hold and recovery behavior.</p>${outageButton}</div><div class="drawer-footnote">The source dataset has no verified fraud outcome. These rules and actions demonstrate workflow only.</div></div>`;
  $("#closeDrawer").onclick = closeDrawer;
  $("#outageButton").onclick = () => { state.outage = state.outage === claim.id ? null : claim.id; renderAll(); };
  $("#drawer").querySelectorAll("[data-decision]").forEach((button) => button.onclick = () => {
    const note = $("#investigationNote").value.trim();
    if (!note) { $("#investigationNote").focus(); $("#investigationNote").classList.add("invalid"); return; }
    state.decisions[claim.id] = { decision: button.dataset.decision, note, at: new Date().toISOString() };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state.decisions));
    renderDrawer();
  });
  if (focusedControl) {
    const replacement = focusedControl.id
      ? document.getElementById(focusedControl.id)
      : [...$("#drawer").querySelectorAll("[data-decision]")].find((button) => button.dataset.decision === focusedControl.dataset.decision);
    (replacement || $("#closeDrawer")).focus({ preventScroll: true });
  }
}

let drawerTrigger = null;
function openDrawer(id) {
  drawerTrigger = document.activeElement;
  state.selected = id;
  $("#scrim").hidden = false;
  $("#drawer").inert = false;
  $("#drawer").classList.add("open");
  $("#drawer").setAttribute("aria-hidden", "false");
  renderDrawer();
  $("#closeDrawer").focus();
  $(".app-shell").inert = true;
}
function closeDrawer() {
  if (!state.selected) return;
  state.selected = null;
  $(".app-shell").inert = false;
  if (drawerTrigger?.isConnected) drawerTrigger.focus({ preventScroll: true });
  else $("#searchInput").focus({ preventScroll: true });
  $("#scrim").hidden = true;
  $("#drawer").classList.remove("open");
  $("#drawer").setAttribute("aria-hidden", "true");
  $("#drawer").inert = true;
}
function renderAll() { renderMetrics(); renderQueue(); renderDrawer(); }

async function init() {
  try {
    const response = await fetch("data/motor_claims.json");
    if (!response.ok) throw new Error(`Could not load claims: ${response.status}`);
    const data = await response.json();
    state.claims = data.claims;
    state.refs = { agents: new Set(data.agents), vendors: new Set(data.vendors) };
    try { state.decisions = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}"); } catch { state.decisions = {}; }
    renderFlow(); renderAll();
  } catch (error) {
    $("#claimRows").innerHTML = `<tr><td colspan="7" class="empty">${escapeHtml(error.message)}. Serve this folder with a local web server.</td></tr>`;
  }
}

$("#searchInput").addEventListener("input", (event) => { state.query = event.target.value.trim(); state.page = 0; renderQueue(); });
$("#outcomeFilter").addEventListener("change", (event) => { state.filter = event.target.value; state.page = 0; renderQueue(); });
$("#sortOrder").addEventListener("change", (event) => { state.sort = event.target.value; state.page = 0; renderQueue(); });
$("#prevPage").onclick = () => { state.page--; renderQueue(); };
$("#nextPage").onclick = () => { state.page++; renderQueue(); };
$("#claimRows").addEventListener("click", (event) => { const row = event.target.closest("tr[data-id]"); if (row) openDrawer(row.dataset.id); });
$("#scrim").onclick = closeDrawer;
document.addEventListener("keydown", (event) => {
  if (!state.selected) return;
  if (event.key === "Escape") closeDrawer();
  if (event.key !== "Tab") return;
  const controls = [...$("#drawer").querySelectorAll("button:not(:disabled), textarea, input, a[href]")];
  const first = controls[0], last = controls.at(-1);
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last?.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first?.focus();
  }
});
$("#resetButton").onclick = () => { localStorage.removeItem(STORAGE_KEY); state.decisions = {}; state.outage = null; renderAll(); };
init();
