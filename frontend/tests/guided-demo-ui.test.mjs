import test from "node:test";
import assert from "node:assert/strict";
import { initGuidedDemo } from "../src/guided-demo-ui.mjs";

test("opening the default page after cleanup restores a working Start demo button", async t => {
  const elements = new Map();
  const element = selector => {
    if (!elements.has(selector)) elements.set(selector, { value: "", hidden: false, disabled: false,
      checked: false, textContent: "", innerHTML: "", querySelectorAll: () => [] });
    return elements.get(selector);
  };
  let stored = JSON.stringify({ version: 1, task: "demo-12345678", actor: "Presenter",
    jobID: "demo-12345678-bootstrap", normalID: "demo-12345678-ordinary", emergingID: "demo-12345678-emerging", submitted: true });
  const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
  const originalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  globalThis.document = { body: { dataset: {} }, querySelector: element };
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
    getItem: () => stored, setItem: (key, value) => { stored = value; }, removeItem: () => { stored = null; }
  } });
  t.after(() => {
    if (originalDocument === undefined) delete globalThis.document; else Object.defineProperty(globalThis, "document", originalDocument);
    if (originalStorage === undefined) delete globalThis.localStorage; else Object.defineProperty(globalThis, "localStorage", originalStorage);
  });
  t.mock.method(globalThis, "fetch", async () => new Response("%PDF-test"));
  const calls = [];
  let scope = "insurance-fraud-demo";
  const ui = initGuidedDemo({ getTask: () => scope, select: async record => { scope = record?.task || "insurance-fraud-demo"; },
    api: async (path, options = {}) => {
      calls.push({ path, ...options });
      if (path === "/policies") return { document: { id: "policy" } };
      if (path === "/datasets") return { id: "dataset", rows: 1000 };
      if (path === "/bootstraps") return { id: options.body.id };
      const error = new Error("record not found"); error.status = 404; throw error;
    }
  });
  assert.equal(element("#guidedStartForm").hidden, true);
  assert.equal(element("#guidedNext").textContent, "Resume demo");
  await ui.refresh();
  assert.equal(stored, null);
  assert.equal(element("#guidedStartForm").hidden, false);
  assert.equal(element("#guidedStart").disabled, false);
  assert.equal(element("#guidedActor").value, "Presenter");
  await element("#guidedStartForm").onsubmit({ preventDefault() {} });
  assert.equal(calls.filter(call => call.path === "/bootstraps").length, 1);
  assert.equal(JSON.parse(stored).submitted, true);
  assert.equal(element("#guidedStartForm").hidden, true);
});
