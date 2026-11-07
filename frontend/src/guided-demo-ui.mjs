import { createGuidedDemo } from "./guided-demo.mjs";
import { demoStory } from "./demo-story.mjs";

const storageKey = "afterthought-guided-demo-v1";
export function initGuidedDemo({ api, getTask, select }) {
  const $ = selector => document.querySelector(selector);
  let saved = null;
  try {
    const value = JSON.parse(localStorage.getItem(storageKey));
    if (value?.version === 1 && /^demo-[a-z0-9-]{8,40}$/.test(value.task) &&
        value.jobID === `${value.task}-bootstrap` && value.normalID === `${value.task}-ordinary` && value.emergingID === `${value.task}-emerging`) saved = value;
  } catch { /* A new demo can replace invalid local state. */ }
  let snapshot, storyHTML = "", selectedTarget = "";
  document.body.dataset.demoView = "story";
  function render(next) {
    snapshot = next;
    const { record, stage, busy, error } = next;
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
    const html = demoStory(next);
    if (html !== storyHTML) {
      const open = [...$("#guidedStory").querySelectorAll("details")].map(node => node.open);
      $("#guidedStory").innerHTML = html; storyHTML = html;
      [...$("#guidedStory").querySelectorAll("details")].forEach((node, index) => { if (index < open.length) node.open = open[index]; });
    }
    $("#guidedStory").hidden = Boolean(elsewhere);
    if (record && !$("#approverActor").value) $("#approverActor").value = record.actor;
    if (record && $("#policyDecisionActor") && !$("#policyDecisionActor").value) $("#policyDecisionActor").value = record.actor;
  }
  const controller = createGuidedDemo({ api, record: saved,
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
  });
  $("#guidedStartForm").onsubmit = async event => {
    event.preventDefault();
    await controller.start($("#guidedActor").value, crypto.randomUUID().slice(0, 8));
  };
  $("#guidedNext").onclick = async () => {
    if (controller.record?.task !== getTask()) { await controller.show("story"); await controller.refresh(); }
    else await controller.advance();
  };
  $("#guidedNew").onclick = async () => { await controller.reset(); selectedTarget = ""; document.body.dataset.demoView = "story"; $("#guidedTechnical").checked = false; };
  $("#guidedTechnical").onchange = event => { document.body.dataset.demoView = event.target.checked ? "workspace" : "story"; };
  return {
    async refresh() {
      if (!controller.record || controller.record.task === getTask()) {
        await controller.refresh();
        // Select the exact candidate once when it becomes reviewable. This also
        // drives the existing approval form and clears stale approval identity.
        const { record, evidence, stage } = snapshot || {};
        const target = stage?.id === "candidate" ? evidence.candidate?.id : stage?.id === "approval" ? evidence.release?.id : "";
        if (target && target !== selectedTarget) {
          selectedTarget = target;
          await select({ ...record, selectedReleaseID: target }, "story");
        }
      } else if (snapshot) render(snapshot);
    },
    scopeChanged() { document.body.dataset.demoView = "workspace"; $("#guidedTechnical").checked = true; if (snapshot) render(snapshot); }
  };
}
