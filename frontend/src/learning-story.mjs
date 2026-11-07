import { escapeHtml as esc, probability } from "./graph-client.mjs";
import { storyGraph } from "./demo-story.mjs";
import { activityMarkup } from "./authoring-trace.mjs";

const roundNames = ["Initial graph", "Round 1 · More history", "Round 2 · New fraud", "Round 3 · Novel pattern"];
const roundLabels = ["Initial graph", "More history", "New fraud", "Novel pattern"];
const number = value => Number.isFinite(value) ? value.toLocaleString() : "—";

function roundStatus(item, activeID) {
  if (item?.job?.status !== "completed" || !item.release) {
    return { label: item?.job?.status || "Planned", tone: item?.job?.status === "failed" ? "warning" : "pending" };
  }
  if (item.release.id === activeID) return { label: "Active", tone: "active" };
  if (item.release.development?.passed && item.release.audit?.passed) return { label: "Eligible for approval", tone: "ready" };
  return { label: "Evaluation gates failed", tone: "warning" };
}

function roundChart(rounds, evidence, selectedIndex) {
  return `<div class="learning-chart" aria-label="Benchmark accuracy by graph version, on a zero to one hundred percent scale">
    <div class="learning-chart-grid" aria-hidden="true">${[100, 75, 50, 25, 0].map(value => `<div><span>${value}%</span></div>`).join("")}</div>
    <div class="learning-bars">${roundNames.map((name, index) => {
      const item = rounds[index];
      const candidate = item?.job?.status === "completed" ? item.release : null;
      const accuracy = candidate?.backtests?.[0]?.metrics?.accuracy;
      const known = Number.isFinite(accuracy);
      const height = known ? Math.max(0, Math.min(100, accuracy * 100)) : 0;
      const status = roundStatus(item, evidence.activeID);
      return `<button type="button" class="learning-round ${known ? "has-result" : "is-pending"} ${index === selectedIndex ? "is-selected" : ""}" data-round-card="${index}" ${candidate ? `data-learning-release="${esc(candidate.id)}" aria-pressed="${index === selectedIndex}"` : "disabled"} title="${esc(name)} · ${esc(status.label)}" aria-label="${esc(name)}, ${known ? `${probability(accuracy)} accuracy` : "awaiting results"}, ${esc(status.label)}">
        <span class="learning-bar-slot" style="--accuracy:${height}%"><span class="learning-bar-value">${known ? probability(accuracy) : "—"}</span><span class="learning-bar-fill"></span></span>
        <span class="learning-round-name">${esc(roundLabels[index])}</span>
        <span class="learning-round-meta"><i class="round-state ${status.tone}" aria-hidden="true"></i>${Number.isFinite(evidence.trainingRows?.[index]) ? `${number(evidence.trainingRows[index])} training rows` : esc(status.label)}</span>
        ${status.tone === "warning" ? `<span class="learning-round-warning">${item?.job.status === "failed" ? "Build failed" : "Gate failed"}</span>` : ""}
      </button>`;
    }).join("")}</div>
  </div>`;
}

function resultsTable(rounds, evidence, selectedIndex) {
  return `<details class="learning-measurements" data-learning-detail="measurements"><summary>Full evaluation results <span>Accuracy, recall & review gates</span></summary><div class="table-scroll"><table class="evaluation-table learning-results"><thead><tr><th>Graph version</th><th>Benchmark accuracy</th><th>Fraud recall</th><th>False alarms</th><th>Original audit accuracy</th><th>Review</th></tr></thead><tbody>${roundNames.map((name, index) => {
    const item = rounds[index], candidate = item?.job?.status === "completed" ? item.release : null;
    const metrics = candidate?.backtests?.[0]?.metrics;
    const status = roundStatus(item, evidence.activeID);
    return `<tr class="${index === selectedIndex ? "is-selected" : ""}"><td>${candidate ? `<button type="button" class="text-button" data-learning-release="${esc(candidate.id)}">${name}</button>` : name}</td><td><strong>${probability(metrics?.accuracy)}</strong>${metrics ? `<small>${metrics.true_positive + metrics.true_negative} / ${metrics.total} correct</small>` : ""}</td><td>${probability(metrics?.recall)}</td><td>${metrics ? `${metrics.false_positive} / ${metrics.false_positive + metrics.true_negative}` : "—"}</td><td>${probability(candidate?.audit?.metrics?.accuracy)}</td><td><span class="learning-state ${status.tone}">${esc(status.label)}</span></td></tr>`;
  }).join("")}</tbody></table></div><p class="learning-footnote">Fraud scores use a 50% threshold. The same balanced benchmark is evaluated each round. Repeated backtests support comparison; a fresh dataset is needed for final generalization claims.</p></details>`;
}

function selectedMetrics(evidence, title, status) {
  const metrics = evidence.release?.backtests?.[0]?.metrics;
  if (!metrics) return "";
  const fraud = metrics.true_positive + metrics.false_negative;
  const legitimate = metrics.true_negative + metrics.false_positive;
  const audit = evidence.release.audit?.metrics;
  const failures = [...(evidence.release.development?.failures || []), ...(evidence.release.audit?.failures || [])];
  return `<section class="learning-selection"><div class="learning-selection-heading"><div><span class="learning-overline">SELECTED VERSION</span><h3>${esc(title)}</h3></div><span class="learning-state ${status.tone}">${esc(status.label)}</span></div>
    <div class="learning-outcomes">
      <article class="learning-outcome"><span>Fraud caught</span><div><strong>${number(metrics.true_positive)}</strong><span>/ ${number(fraud)}</span></div><div class="outcome-track"><span style="width:${metrics.recall * 100}%"></span></div><p><strong>${probability(metrics.recall)}</strong> recall · ${number(metrics.false_negative)} missed</p></article>
      <article class="learning-outcome false-alarms"><span>Legitimate claims flagged</span><div><strong>${number(metrics.false_positive)}</strong><span>/ ${number(legitimate)}</span></div><div class="outcome-track"><span style="width:${legitimate ? metrics.false_positive / legitimate * 100 : 0}%"></span></div><p>${number(metrics.true_negative)} legitimate claims cleared</p></article>
      <article class="learning-outcome original-audit"><span>Original audit accuracy</span><div><strong>${probability(audit?.accuracy)}</strong></div><p>${audit ? `${number(audit.true_positive + audit.true_negative)} of ${number(audit.total)} earlier cases correct` : "Awaiting evaluation"}</p></article>
    </div>${failures.length ? `<p class="learning-gate-note">${esc([...new Set(failures)].join(". "))}</p>` : ""}
  </section>`;
}

function explanationMarkup(evidence, title) {
  const { selectedJob, activity, activityError } = evidence;
  if (!selectedJob) return "";
  const responses = (activity?.transcript || []).filter(message => message.role === "assistant" && typeof message.content === "string" && message.content.trim());
  const explanation = responses.at(-1)?.content || activityError || "Waiting for the agent's first recorded explanation.";
  const lines = explanation.split(/\n+/).filter(line => line.trim());
  const explanationHTML = lines.map(line => `<p>${esc(line.replace(/^\s*-\s+/, "")).replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")}</p>`).join("");
  return `<section class="learning-explanation"><div class="learning-explanation-heading"><span class="learning-agent-mark" aria-hidden="true">A</span><div><h3>Why this version changed</h3><span>${esc(title || "Selected round")} · Recorded agent explanation</span></div></div><div class="learning-explanation-text">${explanationHTML}</div><div class="learning-evidence-links">${selectedJob.prompt ? `<details data-learning-detail="request"><summary>View the instruction</summary><p class="learning-request">${esc(selectedJob.prompt)}</p></details>` : ""}<details class="activity-panel" data-learning-detail="activity"><summary>Model responses and tool activity</summary>${activity ? activityMarkup(activity) : `<p class="form-hint">${esc(activityError || "Loading recorded activity…")}</p>`}</details></div></section>`;
}

function graphMarkup(evidence) {
  const { release, parent, beforeSource, afterSource } = evidence;
  const changed = release?.node_id?.replace(/^fit:/, "");
  return `<section class="learning-workflow"><div class="learning-selection-heading"><div><span class="learning-overline">WORKFLOW</span><h3>${release?.node_id ? `Updated ${esc(release.node_id.replace(/^fit:/, ""))} node` : "Agent-authored graph"}</h3></div><span class="learning-state ${release?.id === evidence.activeID ? "active" : "pending"}">${release ? release.id === evidence.activeID ? "Active · human approved" : "Evaluated candidate" : "Awaiting authoring"}</span></div><div class="learning-graph-grid"><div><h4>Training</h4>${storyGraph(release?.graph?.fit, changed, "Fitted model")}</div><div><h4>Serving</h4>${storyGraph(release?.graph?.serve)}</div></div>${parent && beforeSource && afterSource ? `<details class="learning-code" data-learning-detail="source"><summary>Inspect the changed training node <span>Source & configuration</span></summary><div class="story-source"><div><h4>Previous version</h4><pre class="source-code">${esc(beforeSource.source)}</pre><pre class="source-code">${esc(JSON.stringify(beforeSource.node.config, null, 2))}</pre></div><div><h4>Selected version</h4><pre class="source-code">${esc(afterSource.source)}</pre><pre class="source-code">${esc(JSON.stringify(afterSource.node.config, null, 2))}</pre></div></div></details>` : ""}</section>`;
}

export function learningStory({ record, evidence }) {
  const rounds = evidence.rounds || [];
  const completed = rounds.filter(item => item.job.status === "completed" && item.release?.backtests?.[0]);
  const first = completed[0]?.release.backtests[0].metrics;
  const last = completed.at(-1)?.release.backtests[0].metrics;
  const delta = first && last ? (last.accuracy - first.accuracy) * 100 : null;
  const improvement = delta === null ? "Awaiting recorded results" : `${delta > 0 ? "+" : ""}${delta.toFixed(1)} percentage points`;
  const selectedIndex = rounds.findIndex(item => item.job.id === evidence.selectedJob?.id);
  const selectedTitle = roundNames[selectedIndex] || "Selected version";
  const status = roundStatus(rounds[selectedIndex], evidence.activeID);
  return `<div class="learning-presentation">
    <section class="learning-hero"><div class="learning-headline"><span class="learning-overline">${last ? "LATEST MEASURED RESULT" : "PREPARED LEARNING EXPERIMENT"}</span><h3>${rounds.length ? "Accuracy across learning rounds" : "Planned learning rounds"}</h3><div class="learning-big-number">${last ? probability(last.accuracy) : "—"}</div><span class="learning-improvement ${delta !== null && delta < 0 ? "declined" : ""}">${delta === null ? "Results appear after evaluation" : esc(improvement)}</span><p>${last ? `${number(last.true_positive + last.true_negative)} of ${number(last.total)} reserved claims correct` : "An initial graph and three rounds of new examples."}</p>${first && completed.length > 1 ? `<div class="learning-baseline">Started at <strong>${probability(first.accuracy)}</strong></div>` : ""}</div><div class="learning-chart-panel"><div class="learning-chart-heading"><span>Benchmark accuracy</span><span>${completed.length} / 4 versions evaluated</span></div>${roundChart(rounds, evidence, selectedIndex)}<p>Select a completed version to explore its decisions.</p></div></section>
    <details class="learning-data-summary" data-learning-detail="data"><summary><span><strong>${number(evidence.dataset?.rows || 400)}</strong> initial claims</span><span><strong>3</strong> learning rounds</span><span><strong>${number(evidence.benchmark?.rows || 560)}</strong> reserved test cases</span><span class="learning-data-more">Dataset details</span></summary><p>${esc(record?.sampling || "Prepared from historical claims and two challenge files, with equal numbers of fraud and legitimate labels in the reserved benchmark.")} Duplicate feature vectors and policy/representative identifiers are excluded before splitting.</p></details>
    ${selectedMetrics(evidence, selectedTitle, status)}
    ${resultsTable(rounds, evidence, selectedIndex)}
    ${explanationMarkup(evidence, selectedTitle)}
    ${graphMarkup(evidence)}
  </div>`;
}
