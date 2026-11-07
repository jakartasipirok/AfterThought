export const learningObjective = "Build and test a classifier from the supplied operator-labeled motor insurance claims. Read graph-authoring.md and profile.json. Use exactly one fit node that learns preprocessing and a classifier from its input rows and labels. Use at least two connected serving nodes with a stable preprocessing/model interface, probabilities, investigator recommendations and short reasons. Keep all estimator parameters in the fit node configuration so later rounds can tune only that node and retrain on more data. Compare reasonable sklearn classifiers using development evidence, report fraud recall and false alarms, and preserve row IDs. Do not use direct identifiers. Return candidate.json and tested modules. The external benchmark is withheld. No activation is requested.";

export function learningStage(record, evidence) {
  if (!record) return { id: "new", title: "Watch a graph learn over several rounds", detail: "Start with historical claims, add labeled examples, and compare every revision on one fixed benchmark.", view: "story" };
  if (record.restore && !record.restored) return { id: "loading", title: "Loading your saved experiment", detail: "Fetching the recorded jobs, graph versions and measured results.", view: "story" };
  if (!record.submitted) return { id: "setup", title: "Preparing separate learning and test datasets", detail: "Reserved benchmark cases stay outside the agent workspace.", action: "Resume preparation", view: "story" };
  const latest = evidence.rounds?.at(-1);
  if (latest?.job?.status === "failed") return { id: "failed", title: "This learning round needs attention", detail: latest.job.error || "Inspect the model activity for this round.", view: "graph" };
  if (record.finished) return { id: "candidate", title: "Four graph versions, one fixed benchmark", detail: "Inspect each measured result and graph change. Choose an eligible version below for human approval.", view: "story" };
  return { id: "learning", title: `Learning round ${Math.max(0, (record.jobs?.length || 1) - 1)} of 3`, detail: latest?.job?.phase ? `Current step · ${latest.job.phase.replaceAll("_", " ")}` : "The agent is authoring and testing a candidate. This can take several minutes.", view: "story" };
}

// Evaluation is automatic. This controller never calls an approval endpoint.
export function createLearningDemo({ api, save, select, changed, record = null }) {
  let evidence = { rounds: [] }, busy = false, error = "", bundle;
  const notify = () => changed({ record, evidence, busy, error, stage: learningStage(record, evidence) });
  const persist = () => save(record);
  async function action(work) {
    if (busy) return;
    busy = true; error = ""; notify();
    try { await work(); } catch (failure) { error = failure.message || "The round could not continue."; }
    finally { busy = false; notify(); }
  }
  async function data() { bundle ||= await api("/learning-demo"); return bundle; }
  async function upload(part, evaluationOnly = false) {
    const inputs = await data();
    const body = new FormData();
    body.append("file", new Blob([part.csv], { type: "text/csv" }), "claims.csv");
    body.append("options", JSON.stringify({ name: part.name, task: record.task, target: inputs.target,
      positive_label: inputs.positiveLabel, label_meaning: inputs.labelMeaning, feature_columns: inputs.featureColumns,
      labels_verified: true, synthetic: inputs.synthetic, evaluation_only: evaluationOnly,
      min_precision: .5, min_recall: .3, max_false_positive_rate: .5, threshold: .5 }));
    return api("/datasets", { method: "POST", body, timeout: 90000 });
  }
  async function prepare() {
    const inputs = await data();
    record.sampling = inputs.sampling;
    if (!record.datasetID) { record.datasetID = (await upload(inputs.baseline)).id; await persist(); }
    if (!record.benchmarkID) { record.benchmarkID = (await upload(inputs.benchmark, true)).id; await persist(); }
    record.feedbackIDs ||= [];
    while (record.feedbackIDs.length < inputs.rounds.length) {
      record.feedbackIDs.push((await upload(inputs.rounds[record.feedbackIDs.length])).id);
      await persist();
    }
    const id = `${record.task}-round-0`;
    await api("/bootstraps", { method: "POST", body: { id, task: record.task, dataset_id: record.datasetID,
      backtest_dataset_ids: [record.benchmarkID], prompt: learningObjective, max_turns: 24 } });
    record.jobs = [id]; record.jobID = id; record.submitted = true;
    await persist(); await select(record, "story");
  }
  async function restore() {
    const result = await api(`/bootstraps?${new URLSearchParams({ task: record.task })}`);
    const jobs = (result.jobs || []).filter(job => /^learn-[a-z0-9-]+-round-[0-3]$/.test(job.id)).sort((a, b) => a.id.localeCompare(b.id));
    if (!jobs.length) { record = null; await persist(); await select(null, "story"); return; }
    const datasets = (await api(`/datasets?${new URLSearchParams({ task: record.task })}`)).datasets;
    const inputs = await data();
    const first = jobs[0];
    Object.assign(record, { submitted: true, restored: true, jobs: jobs.map(job => job.id), jobID: jobs.at(-1).id,
      datasetID: first.dataset_id, benchmarkID: first.backtest_dataset_ids[0], sampling: inputs.sampling,
      feedbackIDs: inputs.rounds.map(part => datasets.find(item => item.name === part.name)?.id) });
    if (record.feedbackIDs.some(id => !id)) throw new Error("Saved experiment is missing a learning dataset.");
    await persist(); await select(record, "story");
  }
  async function nextRound() {
    const index = record.jobs.length;
    if (index > 3) { record.finished = true; await persist(); return; }
    const previous = evidence.rounds.at(-1)?.release;
    if (!previous) return;
    const fit = previous.graph?.fit;
    if (fit?.nodes?.length !== 1) throw new Error("This experiment requires one independently tunable fit node. Inspect the authored graph.");
    const id = `${record.task}-round-${index}`;
    await api(`/releases/${encodeURIComponent(previous.id)}/tune`, { method: "POST", body: {
      id, node_id: `fit:${fit.output}`, feedback_dataset_ids: record.feedbackIDs.slice(0, index), max_turns: 24,
      prompt: `Learning round ${index}. Additional operator-labeled examples have been appended to data/train.json and data/development.json. Read learning.json for provenance and row counts. Tune only this fit node, preserving the fitted output interface consumed by every serving node. Select model/preprocessing hyperparameters using training and development evidence. Check data/original-development.json to avoid losing earlier behavior and test the cumulative development data to learn the additional cases. Compare sensible models and class weighting; avoid an always-negative classifier. The runtime will retrain this fit node on the cumulative training input. Keep every other node unchanged. Explain the change and measured development performance in at most three plain-language bullets. External benchmark labels and results are withheld. Activation remains a human decision.`
    } });
    record.jobs.push(id); record.jobID = id; await persist(); await select(record, "story");
  }
  async function refresh({ allowAdvance = true } = {}) {
    if (!record || busy) return;
    await action(async () => {
      if (record.restore && !record.restored) await restore();
      if (!record?.submitted) return;
      const results = [];
      for (const id of record.jobs) results.push(await api(`/bootstraps/${encodeURIComponent(id)}`));
      evidence.rounds = results;
      evidence.activeID = (await api(`/releases?${new URLSearchParams({ task: record.task })}`)).active_release_id;
      evidence.dataset = await api(`/datasets/${encodeURIComponent(record.datasetID)}`);
      evidence.benchmark = await api(`/datasets/${encodeURIComponent(record.benchmarkID)}`);
      evidence.feedbackDatasets ||= await Promise.all(record.feedbackIDs.map(id => api(`/datasets/${encodeURIComponent(id)}`)));
      let trainRows = evidence.dataset.profile?.split_counts?.train?.rows;
      evidence.trainingRows = [trainRows];
      for (const dataset of evidence.feedbackDatasets) { trainRows += dataset.profile?.split_counts?.train?.rows; evidence.trainingRows.push(trainRows); }
      const latest = results.at(-1);
      const selected = results.find(item => item.release?.id === record.selectedReleaseID) || results.filter(item => item.job.status === "completed" && item.release).at(-1) || latest;
      evidence.selectedJob = selected?.job;
      evidence.release = selected?.release;
      evidence.parent = results.find(item => item.release?.id === evidence.release?.parent_release_id)?.release;
      const sessionID = selected?.job.session_id;
      if (sessionID && (evidence.activityID !== sessionID || !evidence.activity || ["queued", "running"].includes(selected.job.status))) {
        evidence.activity = null;
        evidence.activityID = sessionID;
        evidence.activityError = "";
        try { evidence.activity = await api(`/sessions/${encodeURIComponent(sessionID)}`); }
        catch (failure) { evidence.activityError = failure.status === 404 ? "Waiting for the authoring session to start." : failure.message; }
      }
      if (evidence.release && evidence.sourceID !== evidence.release.id && evidence.parent) {
        const [phase, node] = evidence.release.node_id.split(":");
        evidence.beforeSource = await api(`/releases/${encodeURIComponent(evidence.parent.id)}/nodes/${phase}/${encodeURIComponent(node)}`);
        evidence.afterSource = await api(`/releases/${encodeURIComponent(evidence.release.id)}/nodes/${phase}/${encodeURIComponent(node)}`);
        evidence.sourceID = evidence.release.id;
      }
      if (latest?.job.status === "completed" && latest.release) {
        if (record.jobs.length === 4) { record.finished = true; await persist(); }
        else if (allowAdvance) await nextRound();
      }
    });
  }
  notify();
  return {
    get record() { return record; }, refresh,
    async start(actor, suffix) {
      if (record) return;
      if (!actor.trim()) throw new Error("Enter your reviewer name.");
      record = { version: 2, task: `learn-${suffix}`, actor: actor.trim(), jobs: [] };
      await action(async () => { await persist(); await prepare(); });
    },
    async advance() { if (!record?.submitted) await action(prepare); else await refresh(); },
    async reset() { record = null; evidence = { rounds: [] }; await persist(); notify(); },
    async inspect(id) {
      const selected = evidence.rounds.find(item => item.release?.id === id);
      if (!selected) return;
      record.selectedReleaseID = id;
      record.jobID = selected.job.id;
      await persist(); await select(record, "story"); await refresh({ allowAdvance: false });
    },
    show(view) { return select(record, view); },
  };
}
