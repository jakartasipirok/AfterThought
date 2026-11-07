import { escapeHtml as esc } from "./agent-client.mjs";
import { activityLoader } from "./authoring-trace.mjs";

export function initPolicyDemo(api, getTask, refresh) {
  const $ = selector => document.querySelector(selector);
  const activity = activityLoader(api, $("#reviewActivity"));
  let current = null, proposal = null, proposalKey = "", generation = 0, busy = false;
  const status = (message, error = false) => { $("#policyStatus").textContent = message; $("#policyStatus").classList.toggle("error", error); };
  function renderDocument() {
    $("#policySummary").textContent = current ? `${current.title} · v${current.version}${current.synthetic ? " · fictional" : ""}` : "Optional PDF knowledge for the agent";
    $("#policyLinks").innerHTML = current ? `<p><a href="/api/policies/${encodeURIComponent(current.id)}/source">Source PDF ↗</a> · <a href="/api/policies/${encodeURIComponent(current.id)}/text">Current policy v${current.version} ↓</a></p>` : "";
  }
  async function prepareSample() {
    const response = await fetch("assets/demo-company-policy.pdf");
    if (!response.ok) throw new Error("Demo policy PDF is unavailable.");
    const transfer = new DataTransfer();
    transfer.items.add(new File([await response.blob()], "demo-company-policy.pdf", { type: "application/pdf" }));
    $("#policyFile").files = transfer.files;
    $("#policyTitle").value = "Fictional motor claims policy";
    $("#policySynthetic").checked = true;
    $("#companyPolicyPanel").open = true;
    status("Demo policy selected. Enter your name and upload before authoring the graph.");
  }
  $("#useDemoPolicy").onclick = () => prepareSample().catch(error => status(error.message, true));
  $("#policyUploadForm").onsubmit = async event => {
    event.preventDefault(); if (busy) return;
    const task = getTask(), file = $("#policyFile").files[0];
    if (!file) return;
    const body = new FormData(); body.append("file", file);
    body.append("options", JSON.stringify({ task, title: $("#policyTitle").value.trim(), actor: $("#policyActor").value.trim(), synthetic: $("#policySynthetic").checked }));
    busy = true; $("#uploadPolicy").disabled = true; status("Reading policy pages…");
    try {
      await api("/policies", { method: "POST", body, timeout: 30000 });
      if (task === getTask()) { status("Policy uploaded. Future graph authoring and pattern reviews can use it."); await refresh(); }
    } catch (error) { if (task === getTask()) status(error.message, true); }
    finally { busy = false; $("#uploadPolicy").disabled = false; }
  };
  function renderProposal(data) {
    const key = JSON.stringify([data, current?.id]);
    if (key === proposalKey) return;
    proposalKey = key; proposal = data?.proposal || null;
    const host = $("#policyProposal");
    if (!proposal) { host.innerHTML = ""; return; }
    const d = proposal.draft, source = data.policy;
    const pending = proposal.status === "awaiting_policy_review", stale = current?.id !== proposal.policy_id;
    host.innerHTML = `<article class="policy-proposal"><span class="eyebrow">SUSPECTED EMERGING PATTERN · HUMAN JUDGMENT</span><h3>${esc(d.title)}</h3><p class="result-text">${esc(d.hypothesis)}</p><p>${esc(d.rationale)}</p><div class="detail-warning"><strong>What still needs checking</strong><p>${esc(d.uncertainty)}</p></div><details><summary>Policy evidence · version ${source.version}</summary>${d.citations.map(c => `<blockquote><p>${esc(c.quote)}</p><footer>${esc(source.title)} · page ${c.page}</footer></blockquote>`).join("")}</details><h4>Proposed addition</h4><p class="result-text">${esc(d.amendment)}</p><p class="form-hint">Publishing updates the agent’s policy knowledge. Graph revisions require evaluation and your activation decision.</p>${pending ? `<form id="policyDecisionForm" autocomplete="off">${stale ? '<p class="detail-warning">The current policy changed. Dismiss this stale proposal and review fresh evidence.</p>' : ""}<label>Your assessment<select id="patternVerdict"><option value="needs_investigation">Needs investigation</option><option value="suspected_scam">Suspected scam pattern</option><option value="benign_change">Benign change / data issue</option></select></label><label>Decision note<textarea id="policyDecisionNote" rows="2" maxlength="2000" required placeholder="What supports your judgment, and what should investigators check?"></textarea></label><div class="form-grid"><label>Reviewed by<input id="policyDecisionActor" maxlength="128" required></label></div><p class="form-hint">Clicking a decision records your review of this proposal.</p><div class="form-actions"><button class="secondary-button" value="dismiss">Dismiss proposal</button><button class="primary-button" value="approve" ${stale ? "disabled" : ""}>Approve policy amendment</button></div><div id="policyDecisionStatus" role="status" class="form-status"></div></form>` : `<p><strong>${esc(proposal.status)}</strong> by ${esc(proposal.actor)} · ${esc(proposal.verdict.replaceAll("_", " "))}</p><p>${esc(proposal.note)}</p>`}</article>`;
    if (!pending) return;
    $("#policyDecisionForm").onsubmit = async event => {
      event.preventDefault(); if (busy) return;
      const task = getTask(), id = proposal.id;
      const body = { decision: event.submitter?.value, verdict: $("#patternVerdict").value, note: $("#policyDecisionNote").value.trim(), actor: $("#policyDecisionActor").value.trim(), confirmed: true, expected_policy_id: current?.id || "" };
      busy = true; const buttons = [...event.target.querySelectorAll("button")]; buttons.forEach(button => button.disabled = true);
      try { await api(`/policy-proposals/${encodeURIComponent(id)}/decision`, { method: "POST", body }); if (task === getTask()) await refresh(); }
      catch (error) { if (task === getTask() && $("#policyDecisionStatus")) $("#policyDecisionStatus").textContent = `${error.message} Review the proposal before retrying.`; }
      finally { busy = false; buttons.forEach(button => button.disabled = button.value === "approve" && stale); }
    };
  }
  async function load(review) {
    const task = getTask(), request = ++generation;
    const id = review?.kind === "emerging_pattern" && ["awaiting_policy_review", "approved", "dismissed"].includes(review.status) ? review.id : null;
    const results = await Promise.allSettled([api(`/policies?${new URLSearchParams({ task })}`), id ? api(`/policy-proposals/${encodeURIComponent(id)}`) : Promise.resolve(null)]);
    if (task !== getTask() || request !== generation) return;
    if (results[0].status === "fulfilled") { current = results[0].value.document; renderDocument(); }
    else status(results[0].reason.message, true);
    if (results[1].status === "fulfilled") renderProposal(results[1].value);
    else status(results[1].reason.message, true);
    await activity(review?.session_id);
  }
  function reset() { generation++; current = null; renderDocument(); renderProposal(null); activity(null); status(""); }
  return { load, reset, prepareSample };
}
