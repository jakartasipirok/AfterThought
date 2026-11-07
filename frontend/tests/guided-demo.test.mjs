import test from "node:test";
import assert from "node:assert/strict";
import { createGuidedDemo, demoStage } from "../src/guided-demo.mjs";
import { comparisonMarkup, demoStory } from "../src/demo-story.mjs";

function fixture() {
  const requests = [], selections = [], snapshots = [];
  let saved, activeID = "", review = null, revisionStarted = false, lostAcknowledgement = false;
  const report = { passed: true, metrics: { precision: .95, recall: .9, f1: .924 } };
  const release = { id: "release-fixture", development: report, audit: report, graph: { serve: { output: "route", nodes: [{ id: "route", needs: [] }] } } };
  const candidate = { ...release, id: "revision-fixture", parent_release_id: release.id, node_id: "serve:route" };
  const runs = new Map(), task = "demo-12345678";
  async function api(path, options = {}) {
    requests.push({ path, ...options });
    if (path === "/policies" || path.startsWith("/policies?")) return { document: { id: "policy-fixture", version: 1 } };
    if (path.startsWith("/datasets")) return { id: "dataset-fixture", rows: 1000 };
    if (path === "/bootstraps") return { id: options.body.id };
    if (path.endsWith("/tune")) { revisionStarted = true; return { id: options.body.id }; }
    if (path.startsWith("/bootstraps/")) {
      const isRevision = path.endsWith("-policy-revision");
      return { job: { status: "completed", release_id: isRevision ? candidate.id : release.id }, release: isRevision ? candidate : release };
    }
    if (path.startsWith("/releases?")) return { active_release_id: activeID };
    if (path.includes("/nodes/")) return { source: path.includes(candidate.id) ? "revised_source" : "original_source", node: { config: {} } };
    if (path.endsWith("/runs")) {
      const { id } = JSON.parse(options.body.get("options"));
      runs.set(id, { id, status: "completed", row_count: 1000, release_id: path.split("/")[2], model_calls: 0, input_tokens: 0, output_tokens: 0 });
      if (lostAcknowledgement) { lostAcknowledgement = false; throw new Error("Lost acknowledgement"); }
      return { id };
    }
    if (path.startsWith("/graph-runs/")) return { run: runs.get(path.split("/").at(-1)), preview: [] };
    if (path === "/auto-review/run") {
      review = { id: "review-fixture", kind: "emerging_pattern", pattern_batch_id: `${task}-emerging`, status: "awaiting_policy_review" };
      return { review };
    }
    if (path.startsWith("/auto-review?")) return { latest: review };
    throw new Error(`Unexpected request ${path}`);
  }
  function controller(record = null) {
    return createGuidedDemo({ record, api, loadPDF: async () => "%PDF-fixture", save: value => { saved = structuredClone(value); },
      select: (...args) => selections.push(args), changed: snapshot => snapshots.push(structuredClone(snapshot)) });
  }
  return { controller, requests, snapshots, selections, runs, task, release, candidate,
    get saved() { return saved; }, get revisionStarted() { return revisionStarted; },
    activate(id = release.id) { activeID = id; }, approvePolicy() { review.status = "approved"; }, dismissPolicy() { review.status = "dismissed"; },
    loseAcknowledgement() { lostAcknowledgement = true; } };
}

async function readyForPolicy(f, guide) {
  await guide.start("Test presenter", "12345678");
  await guide.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "approval");
  assert.equal(f.runs.size, 0);
  f.activate();
  for (let i = 0; i < 4; i++) await guide.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "ready");
}

test("browser demo automates preparation and node revision, pausing for each human decision", async () => {
  const f = fixture(), first = f.controller();
  await readyForPolicy(f, first);
  assert.equal(f.revisionStarted, false);
  const restored = f.controller(f.saved);
  await restored.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "ready");
  f.approvePolicy();
  await restored.refresh();
  assert.equal(f.revisionStarted, true);
  await restored.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "candidate");
  assert.equal(f.runs.size, 2);
  f.activate(f.candidate.id);
  await restored.refresh(); await restored.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "complete");
  assert.equal(f.saved.autoPrepare, false);
  assert.equal(f.runs.size, 3);
  assert.equal(f.requests.filter(r => r.path === "/bootstraps").length, 1);
  assert.equal(f.requests.filter(r => r.path.endsWith("/tune")).length, 1);
  assert.ok(f.requests.every(r => !r.path.endsWith("/approve") && !r.path.endsWith("/decision")));
  assert.equal(f.requests.find(r => r.path === "/datasets").body.get("file").type, "text/csv");
  assert.ok(!JSON.stringify(f.saved).includes("approval_code"));
});

test("retry after an uncertain batch submission keeps its stable request identity", async () => {
  const f = fixture(), guide = f.controller();
  await guide.start("Test presenter", "12345678");
  f.activate(); f.loseAcknowledgement(); await guide.refresh();
  assert.match(f.snapshots.at(-1).error, /Lost acknowledgement/);
  assert.equal(f.saved.autoPrepare, false);
  await guide.advance();
  const ids = f.requests.filter(r => r.path.endsWith("/runs")).map(r => JSON.parse(r.body.get("options")).id);
  assert.deepEqual(ids, [`${f.task}-ordinary`, `${f.task}-ordinary`]);
});

test("failed evaluation and unexpected serving counters stop automatic preparation", async () => {
  const f = fixture(), guide = f.controller();
  await guide.start("Test presenter", "12345678"); f.activate(); await guide.refresh();
  f.runs.get(`${f.task}-ordinary`).model_calls = 1;
  await guide.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "failed");
  assert.equal(f.saved.autoPrepare, false);
  assert.equal(f.runs.size, 1);
  assert.equal(demoStage({ submitted: true }, { job: { status: "completed" }, release: { development: { passed: false } } }).id, "failed");
});

test("dismissing a policy hypothesis ends the demo without a graph revision", async () => {
  const f = fixture(), guide = f.controller();
  await readyForPolicy(f, guide); f.dismissPolicy(); await guide.refresh();
  assert.equal(f.snapshots.at(-1).stage.id, "complete");
  assert.equal(f.revisionStarted, false);
});

test("comparison renders actual equal, lower and unavailable metrics without invented gains", () => {
  const before = { audit: { metrics: { f1: .9, precision: .8 } } };
  const after = { audit: { metrics: { f1: .9, precision: .7 } } };
  const html = comparisonMarkup(before, after);
  assert.match(html, /90.0%<\/td><td>90.0%/);
  assert.match(html, /80.0%<\/td><td>70.0%/);
  assert.match(html, /Not reported/);
  const story = demoStory({ record: null, evidence: { policy: { title: '<script>bad</script>', version: 1, id: 'policy' } }, stage: { id: 'new' } });
  assert.ok(!story.includes('<script>'));
  assert.match(story, /&lt;script&gt;/);
});

test("restored progress is published immediately before polling another scope", () => {
  const f = fixture();
  const record = { version: 1, task: f.task, actor: "Presenter", jobID: `${f.task}-bootstrap`, submitted: true };
  f.controller(record);
  assert.equal(f.snapshots.length, 1);
  assert.equal(f.snapshots[0].record.task, f.task);
  assert.equal(f.requests.length, 0);
});

test("archived saved progress resets to a usable start form after a confirmed missing job", async () => {
  const snapshots = [], selections = [];
  let saved = "unchanged";
  const controller = createGuidedDemo({
    record: { task: "demo-archived", jobID: "demo-archived-bootstrap", submitted: true },
    api: async () => { const error = new Error("record not found"); error.status = 404; throw error; },
    loadPDF: async () => "", save: record => { saved = record; },
    select: (...args) => selections.push(args), changed: snapshot => snapshots.push(structuredClone(snapshot))
  });
  await controller.refresh({ allowAdvance: false });
  assert.equal(controller.record, null);
  assert.equal(saved, null);
  assert.equal(snapshots.at(-1).stage.id, "new");
  assert.deepEqual(selections, [[null, "story"]]);
});

test("temporary lookup failure retains saved progress, and checking another scope starts no jobs", async () => {
  const record = { task: "demo-saved", jobID: "demo-saved-bootstrap", submitted: true };
  const controller = createGuidedDemo({ record, api: async () => { throw new Error("Offline"); },
    loadPDF: async () => "", save: () => {}, select: () => {}, changed: () => {} });
  await controller.refresh({ allowAdvance: false });
  assert.equal(controller.record.jobID, record.jobID);
  const f = fixture(), guide = f.controller();
  await guide.start("Presenter", "12345678");
  f.activate();
  await guide.refresh({ allowAdvance: false });
  assert.equal(f.runs.size, 0);
});
