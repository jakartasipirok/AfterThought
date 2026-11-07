import { createPolicyDemoCSV } from "./demo-data.mjs";

const objective = "Build a reproducible classifier for the synthetic motor-claims dataset. Read graph-authoring.md, profile.json and knowledge/company-policy.json. Inspect training/development evidence and choose an appropriate model. Author distinct fit and serving nodes with tunable configuration and focused tests. Return valid probabilities, investigator-routing recommendations and grounded reasons. Handle finite evidence_reuse_count values beyond the training range without crashing. Use the policy's investigation procedures. Keep each node independently revisable. The synthetic target demonstrates mechanics. Do not attempt activation.";

export function demoStage(record, evidence = {}) {
  if (!record) return { id: "new", title: "From claims to a learning workflow", detail: "Give the agent a dataset and company knowledge. Watch it build, review and improve a reusable graph.", action: "Start guided demo", view: "build" };
  if (!record.submitted) return { id: "setup", title: "Preparing the dataset and policy", detail: "Upload the demo inputs and start graph authoring.", action: "Resume preparation", view: "build" };
  const { job, release, activeID, normal, emerging, review, revisionJob, candidate, improved } = evidence;
  if (!job || ["queued", "running"].includes(job.status)) return { id: "building", title: "The agent is building your graph", detail: job?.phase ? `Current phase · ${job.phase}` : "Waiting for the authoring worker.", view: "build" };
  if (job.status !== "completed" || !release || !release.development?.passed || !release.audit?.passed) return { id: "failed", title: "The graph needs attention", detail: job.error || release?.execution_error || "Inspect the retained activity and evaluation before continuing.", action: "Inspect graph", view: "graph" };
  if (activeID !== release.id && (!candidate || activeID !== candidate.id)) return { id: "approval", title: "Your graph is ready for approval", detail: "Inspect the nodes and measured evaluation, then approve activation below.", view: "story" };
  for (const [run, name] of [[normal, "ordinary"], [emerging, "emerging-pattern"]]) {
    if (run?.status === "failed") return { id: "failed", title: `The ${name} batch needs attention`, detail: run.error || "Inspect the node execution trace.", action: "Inspect batch", view: "run" };
    if (run?.status === "completed" && (run.release_id !== release.id || run.row_count !== 1000 || run.model_calls !== 0 || run.input_tokens !== 0 || run.output_tokens !== 0)) return { id: "failed", title: "Serving evidence needs inspection", detail: "The returned release, row count or serving counters differ from this demo's expected result.", action: "Inspect batch", view: "run" };
  }
  if (!normal || normal.status !== "completed") return { id: "normal", title: "Run 1,000 claims through your graph", detail: normal ? "The approved graph is processing the ordinary batch." : "The approved graph will process ordinary claims and a controlled emerging-pattern batch.", action: normal ? null : "Continue learning", view: "run" };
  if (!emerging || emerging.status !== "completed") return { id: "emerging", title: "Testing an emerging pattern", detail: "The demo raises evidence reuse from 0–2 to 8–12. The serving graph processes the batch.", action: emerging ? null : "Continue preparation", view: "run" };
  if (!review) return { id: "review", title: "Investigate the new signal", detail: "Both batches finished with zero serving tokens. Request a cited policy proposal.", action: "Continue preparation", view: "review" };
  if (review.kind !== "emerging_pattern" || review.pattern_batch_id !== record.emergingID) return { id: "failed", title: "Review evidence needs inspection", detail: "The latest review does not match the demo's emerging batch.", action: "Inspect review", view: "review" };
  if (["queued", "running"].includes(review.status)) return { id: "reviewing", title: "The agent is reviewing company policy", detail: "Follow the model responses and tools while it authors a cited proposal.", view: "review" };
  if (review.status === "awaiting_policy_review") return { id: "ready", title: "The agent proposes a policy update", detail: "Review the evidence and proposed policy addition. After you publish it, the agent will propose a one-node graph revision.", view: "story" };
  if (review.status === "dismissed") return { id: "complete", title: "Your decision is recorded", detail: "The dismissed hypothesis and your assessment are retained as learning evidence.", view: "story" };
  if (review.status === "approved") {
    if (!record.revisionSubmitted) return { id: "revise", title: "Applying your approved policy to the graph", detail: "The agent will revise the serving output node and evaluate the candidate.", action: "Continue learning", view: "story" };
    if (!revisionJob || ["queued", "running"].includes(revisionJob.status)) return { id: "revising", title: "The agent is proposing a graph change", detail: "One node is being revised against the approved company policy.", view: "story" };
    if (revisionJob.status !== "completed" || !candidate || !candidate.development?.passed || !candidate.audit?.passed) return { id: "failed", title: "The proposed revision needs attention", detail: revisionJob.error || candidate?.execution_error || "Inspect the evaluation before requesting another revision.", action: "Inspect revision", view: "graph" };
    if (activeID !== candidate.id) return { id: "candidate", title: "A graph change is ready for your review", detail: "Compare the changed node and measured results, then approve activation.", view: "story" };
    if (improved?.status === "failed") return { id: "failed", title: "The revised batch needs attention", detail: improved.error || "Inspect the revised graph execution.", view: "story" };
    if (improved?.status === "completed") {
      if (improved.release_id !== candidate.id || improved.row_count !== 1000 || improved.model_calls !== 0 || improved.input_tokens !== 0 || improved.output_tokens !== 0) return { id: "failed", title: "Revised serving evidence needs inspection", detail: "The returned release, row count or serving counters differ from the expected result.", view: "story" };
      return { id: "complete", title: "Your revised graph is running", detail: "Compare the evaluation and the same emerging-pattern batch before and after your decision.", view: "story" };
    }
    return { id: "verify", title: "Verifying the activated graph", detail: "The same emerging-pattern batch is running through your approved revision.", action: "Verify revision", view: "story" };
  }
  return { id: "failed", title: "Policy review needs attention", detail: `Review ended as ${review.status}. Inspect its activity before another attempt.`, action: "Inspect review", view: "review" };
}

// This controller only admits authoring, serving and review requests. The existing
// human approval forms perform graph activation and policy publication.
export function createGuidedDemo({ api, loadPDF, save, select, changed, record = null }) {
  let evidence = {}, busy = false, error = "";
  const data = createPolicyDemoCSV();
  const notify = () => changed({ record, evidence, busy, error, stage: demoStage(record, evidence) });
  async function persist() { await save(record); }
  async function upload(path, content, name, options, type = "text/csv") {
    const body = new FormData();
    body.append("file", new Blob([content], { type }), name);
    body.append("options", JSON.stringify(options));
    return api(path, { method: "POST", body, timeout: 90000 });
  }
  async function action(work, clearError = true) {
    if (busy) return;
    busy = true; if (clearError) error = ""; notify();
    try { await work(); }
    catch (failure) {
      error = failure.message || "Preparation failed. Retry to resume the same requests.";
      if (record) {
        record.autoPrepare = false;
        try { await persist(); } catch { error += " Browser progress could not be saved."; }
      }
    } finally { busy = false; notify(); }
  }
  async function bootstrap() {
    if (!record.policyID) {
      const result = await upload("/policies", await loadPDF(), "demo-company-policy.pdf", {
        task: record.task, actor: record.actor, title: "Fictional motor claims policy", synthetic: true
      }, "application/pdf");
      evidence.policy = result.document; record.policyID = result.document.id; await persist();
    }
    if (!record.datasetID) {
      const dataset = await upload("/datasets", data.evaluationCSV, "synthetic-motor-claims.csv", {
        task: record.task, name: "Synthetic motor claims", target: data.target, positive_label: data.positiveLabel,
        label_meaning: data.labelMeaning, feature_columns: data.featureColumns, synthetic: true, labels_verified: true,
        min_precision: .85, min_recall: .85, max_false_positive_rate: .15, threshold: .5
      });
      evidence.dataset = dataset; record.datasetID = dataset.id; await persist();
    }
    await api("/bootstraps", { method: "POST", body: { id: record.jobID, task: record.task,
      dataset_id: record.datasetID, policy_id: record.policyID, prompt: objective, max_turns: 20 } });
    record.submitted = true; await persist(); await select(record, "story");
  }
  async function runNext() {
    const stage = demoStage(record, evidence);
    if (stage.id === "normal" || stage.id === "emerging") {
      const ordinary = stage.id === "normal", id = ordinary ? record.normalID : record.emergingID;
      await upload(`/releases/${encodeURIComponent(record.releaseID)}/runs`, ordinary ? data.inferenceCSV : data.emergingCSV, `${id}.csv`, { id });
      record[ordinary ? "normalSubmitted" : "emergingSubmitted"] = true;
      await persist(); await select(record, "story", id);
    } else if (stage.id === "review") {
      const current = await api(`/auto-review?${new URLSearchParams({ task: record.task })}`);
      evidence.review = current.latest;
      if (!current.latest) evidence.review = (await api("/auto-review/run", { method: "POST", body: { task: record.task } })).review;
      await select(record, "story", record.emergingID);
    } else if (stage.id === "revise") {
      const output = evidence.release.graph?.serve?.output;
      if (!output) throw new Error("The approved graph has no serving output node to revise.");
      record.revisionNode = `serve:${output}`;
      record.revisionID ||= `${record.task}-policy-revision`;
      await persist();
      await api(`/releases/${encodeURIComponent(record.releaseID)}/tune`, { method: "POST", body: {
        id: record.revisionID, node_id: record.revisionNode, max_turns: 20,
        prompt: `Apply the human-approved company policy amendment from policy review ${evidence.review.id} to this serving output node. Read knowledge/company-policy.json and the existing node source. Implement grounded investigator-routing reasons and evidence-provenance checks for the observed evidence_reuse_count range 8–12 versus training 0–2. Preserve probability calibration unless verified labelled evidence supports a change. Treat the pattern as a hypothesis for human investigation. Include focused tests for ordinary and emerging inputs. Explain exactly what changed and what your tests establish. Only this node may change.`
      } });
      record.revisionSubmitted = true; await persist(); await select(record, "story");
    } else if (stage.id === "verify" && !record.improvedSubmitted) {
      record.improvedID ||= `${record.task}-improved`;
      await persist();
      await upload(`/releases/${encodeURIComponent(evidence.candidate.id)}/runs`, data.emergingCSV, "emerging-pattern-replay.csv", { id: record.improvedID });
      record.improvedSubmitted = true; await persist();
    }
  }
  async function refresh() {
    if (!record?.submitted || busy) { notify(); return; }
    await action(async () => {
      const job = await api(`/bootstraps/${encodeURIComponent(record.jobID)}`);
      const channel = await api(`/releases?${new URLSearchParams({ task: record.task })}`);
      evidence.job = job.job;
      evidence.release = job.release;
      evidence.activeID = channel.active_release_id;
      evidence.dataset = await api(`/datasets/${encodeURIComponent(record.datasetID)}`);
      evidence.policy = (await api(`/policies?${new URLSearchParams({ task: record.task })}`)).document;
      if (job.job.release_id && !evidence.release) evidence.release = (await api(`/releases/${encodeURIComponent(job.job.release_id)}`)).release;
      if (evidence.release && record.releaseID !== evidence.release.id) { record.releaseID = evidence.release.id; await persist(); }
      if (record.normalSubmitted) evidence.normal = (await api(`/graph-runs/${encodeURIComponent(record.normalID)}`)).run;
      if (record.emergingSubmitted) {
        const batch = await api(`/graph-runs/${encodeURIComponent(record.emergingID)}`);
        evidence.emerging = batch.run; evidence.beforePreview = batch.preview;
      }
      if (record.revisionSubmitted) {
        const revision = await api(`/bootstraps/${encodeURIComponent(record.revisionID)}`);
        evidence.revisionJob = revision.job; evidence.candidate = revision.release;
        if (revision.job.release_id && !evidence.candidate) evidence.candidate = (await api(`/releases/${encodeURIComponent(revision.job.release_id)}`)).release;
        if (evidence.candidate) {
          if (evidence.candidate.parent_release_id !== record.releaseID || evidence.candidate.node_id !== record.revisionNode) throw new Error("Revision does not match the requested parent and node.");
          if (record.candidateID !== evidence.candidate.id) { record.candidateID = evidence.candidate.id; await persist(); }
          if (evidence.sourceCandidate !== evidence.candidate.id) {
            const [phase, node] = record.revisionNode.split(":");
            evidence.beforeSource = await api(`/releases/${encodeURIComponent(record.releaseID)}/nodes/${phase}/${encodeURIComponent(node)}`);
            evidence.afterSource = await api(`/releases/${encodeURIComponent(record.candidateID)}/nodes/${phase}/${encodeURIComponent(node)}`);
            evidence.sourceCandidate = record.candidateID;
          }
        }
      }
      if (record.improvedSubmitted) {
        const batch = await api(`/graph-runs/${encodeURIComponent(record.improvedID)}`);
        evidence.improved = batch.run; evidence.afterPreview = batch.preview;
      }
      if (evidence.emerging?.status === "completed") evidence.review = (await api(`/auto-review?${new URLSearchParams({ task: record.task })}`)).latest;
      const stage = demoStage(record, evidence);
      if (["complete", "failed"].includes(stage.id) && record.autoPrepare) { record.autoPrepare = false; await persist(); }
      if (record.autoPrepare && ["normal", "emerging", "review", "revise", "verify"].includes(stage.id)) {
        const waiting = stage.id === "normal" ? evidence.normal : evidence.emerging;
        if (["review", "revise", "verify"].includes(stage.id) || !waiting) await runNext();
      }
    }, false);
  }
  return {
    get record() { return record; },
    refresh,
    async start(actor, suffix) {
      if (record) return;
      if (!actor.trim() || actor.length > 128) throw new Error("Enter your name to attribute the policy upload.");
      const task = `demo-${suffix}`;
      record = { version: 1, task, actor: actor.trim(), jobID: `${task}-bootstrap`, normalID: `${task}-ordinary`, emergingID: `${task}-emerging`, autoPrepare: true };
      await action(async () => { await persist(); await select(record, "story"); await bootstrap(); });
    },
    async advance() {
      if (!record) return;
      await action(async () => {
        const stage = demoStage(record, evidence);
        if (!record.submitted) { record.autoPrepare = true; await bootstrap(); }
        else if (["normal", "emerging", "review", "revise", "verify"].includes(stage.id)) {
          record.autoPrepare = true; await persist(); await runNext();
        } else await select(record, stage.view, stage.view === "run" && record.normalSubmitted ? record.normalID : undefined);
      });
    },
    async reset() { await action(async () => { record = null; evidence = {}; await persist(); }); },
    show(view) {
      if (!record) return;
      const run = view === "run" && record.normalSubmitted ? record.normalID : undefined;
      return select(record, view, run);
    }
  };
}
