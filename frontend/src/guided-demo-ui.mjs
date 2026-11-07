import { createGuidedDemo } from "./guided-demo.mjs";
import { createLearningDemo, learningStage } from "./learning-demo.mjs";
import { learningStory } from "./learning-story.mjs";
import { demoStory } from "./demo-story.mjs";

const storageKey = "afterthought-guided-demo-v1";
export function initGuidedDemo({ api, getTask, select }) {
  const $ = selector => document.querySelector(selector);
  let saved = null;
  try {
    const value = JSON.parse(localStorage.getItem(storageKey));
    if (value?.version === 2 && /^learn-[a-z0-9-]{8,40}$/.test(value.task)) saved = { ...value, restore: true, restored: false };
    if (value?.version === 1 && /^demo-[a-z0-9-]{8,40}$/.test(value.task) &&
        value.jobID === `${value.task}-bootstrap` && value.normalID === `${value.task}-ordinary` && value.emergingID === `${value.task}-emerging`) saved = value;
  } catch { /* A new demo can replace invalid local state. */ }
  if (typeof location !== "undefined") {
    const task = new URLSearchParams(location.search).get("task");
    if (/^learn-[a-z0-9-]{8,40}$/.test(task) && task !== saved?.task) saved = { version: 2, task, actor: "", restore: true };
  }
  let snapshot, storyHTML = "", selectedTarget = "", checkedSavedScope = false;
  document.body.dataset.demoView = "story";
  function render(next) {
    snapshot = next;
    const { record, busy, error } = next;
    const learning = record?.version === 2 || (!record && $("#guidedMode")?.value === "learning");
    const stage = learning && !record ? learningStage(null, {}) : next.stage;
    const elsewhere = record && record.task !== getTask();
    $("#guidedStartForm").hidden = Boolean(record);
    $("#guidedControls").hidden = !record;
    $("#guidedTitle").textContent = elsewhere ? "Resume your saved demo" : stage.title;
    $("#guidedDetail").textContent = elsewhere ? `Open ${record.task} to continue.` : stage.detail;
    $("#guidedError").textContent = error;
    $("#guidedError").hidden = !error;
    $("#guidedStart").disabled = busy;
    const primary = $("#guidedNext");
    primary.textContent = elsewhere ? "Resume demo" : error ? "Retry preparation" : stage.action || "Continue learning";
    primary.hidden = !elsewhere && !error && (!stage.action || record?.autoPrepare);
    primary.disabled = busy;
    $("#guidedNew").disabled = busy;
    document.body.dataset.demoStage = elsewhere ? "elsewhere" : stage.id;
    const html = learning ? learningStory(next) : demoStory(next);
    if (html !== storyHTML) {
      const open = [...$("#guidedStory").querySelectorAll("details")].map(node => node.open);
      $("#guidedStory").innerHTML = html; storyHTML = html;
      [...$("#guidedStory").querySelectorAll("details")].forEach((node, index) => { if (index < open.length) node.open = open[index]; });
    }
    $("#guidedStory").hidden = Boolean(elsewhere);
    if (record && !$("#guidedActor").value) $("#guidedActor").value = record.actor;
    if (record && !$("#approverActor").value) $("#approverActor").value = record.actor;
    if (record && $("#policyDecisionActor") && !$("#policyDecisionActor").value) $("#policyDecisionActor").value = record.actor;
  }
  const controllerOptions = { api, record: saved,
    loadPDF: async () => {
      const response = await fetch("assets/demo-company-policy.pdf");
      if (!response.ok) throw new Error("The sample policy PDF could not be loaded.");
      return response.arrayBuffer();
    },
    save: record => { if (record) localStorage.setItem(storageKey, JSON.stringify(record)); else localStorage.removeItem(storageKey); },
    select: async (record, name, run) => {
      await select(record, name, run);
      document.body.dataset.demoView = name === "story" ? "story" : "workspace";
      $("#guidedTechnical").checked = name !== "story";
    },
    changed: render
  };
  let controller = saved?.version === 2 ? createLearningDemo(controllerOptions) : createGuidedDemo(controllerOptions);
  if ($("#guidedMode")) $("#guidedMode").onchange = () => { if (snapshot) render(snapshot); };
  $("#guidedStartForm").onsubmit = async event => {
    event.preventDefault();
    if (!controller.record && $("#guidedMode")?.value === "learning") controller = createLearningDemo({ ...controllerOptions, record: null });
    else if (!controller.record) controller = createGuidedDemo({ ...controllerOptions, record: null });
    await controller.start($("#guidedActor").value, crypto.randomUUID().slice(0, 8));
  };
  $("#guidedStory").onclick = async event => {
    const button = event.target.closest("[data-learning-release]");
    if (button && controller.inspect) await controller.inspect(button.dataset.learningRelease);
  };
  $("#guidedNext").onclick = async () => {
    if (controller.record?.task !== getTask()) { await controller.show("story"); await controller.refresh(); }
    else await controller.advance();
  };
  $("#guidedNew").onclick = async () => {
    await controller.reset();
    selectedTarget = "";
    await select(null, "story");
    document.body.dataset.demoView = "story";
    $("#guidedTechnical").checked = false;
  };
  $("#guidedTechnical").onchange = event => { document.body.dataset.demoView = event.target.checked ? "workspace" : "story"; };
  return {
    async refresh() {
      if (!checkedSavedScope && controller.record?.task !== getTask()) {
        checkedSavedScope = true;
        await controller.refresh({ allowAdvance: false });
      }
      if (!controller.record || controller.record.task === getTask()) {
        await controller.refresh();
        // Select the exact candidate once when it becomes reviewable. This also
        // drives the existing approval form and clears stale approval identity.
        const { record, evidence, stage } = snapshot || {};
        const target = record?.version === 2 && stage?.id === "candidate" ? evidence.release?.id : stage?.id === "candidate" ? evidence.candidate?.id : stage?.id === "approval" ? evidence.release?.id : "";
        if (target && target !== selectedTarget) {
          selectedTarget = target;
          await select({ ...record, selectedReleaseID: target }, "story");
        }
      } else if (snapshot) render(snapshot);
    },
    scopeChanged() { document.body.dataset.demoView = "workspace"; $("#guidedTechnical").checked = true; if (snapshot) render(snapshot); }
  };
}
