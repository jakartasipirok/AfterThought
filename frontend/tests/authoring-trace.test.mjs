import test from "node:test";
import assert from "node:assert/strict";
import { activityMarkup } from "../src/authoring-trace.mjs";
import { createPolicyDemoCSV } from "../src/demo-data.mjs";

test("activity shows all public responses in order, joins tool results, and escapes content", () => {
 const html = activityMarkup({ transcript: [
  { role: "system", content: "hidden instructions" },
  { role: "assistant", content: "First <script>bad()</script>", tool_calls: [{id:"one",function:{name:"read",arguments:'{"path":"policy.json"}'}}] },
  { role: "tool",tool_call_id:"one",content:'{"output":"source quote"}' },
  { role: "assistant",content:"Second response" },
 ], usage:[{input_tokens:20,output_tokens:10}] });
 assert.ok(html.indexOf("First") < html.indexOf("source quote"));
 assert.ok(html.indexOf("source quote") < html.indexOf("Second response"));
 assert.ok(!html.includes("<script>") && !html.includes("hidden instructions"));
 assert.match(html,/&lt;script&gt;/); assert.match(html,/Model calls/);assert.match(html,/>20</);
});

test("insurance demo shifts only reuse counts and withholds synthetic labels from serving", () => {
 const d=createPolicyDemoCSV(); assert.deepEqual(d,createPolicyDemoCSV());
 const training=d.evaluationCSV.trim().split("\n"), ordinary=d.inferenceCSV.trim().split("\n"), emerging=d.emergingCSV.trim().split("\n");
 assert.equal(training.length,1001);assert.equal(ordinary.length,1001);assert.equal(emerging.length,1001);
 assert.ok(!emerging[0].includes(d.target));
 for(let i=1;i<emerging.length;i++) {
  const a=ordinary[i].split(","),b=emerging[i].split(",");
  assert.deepEqual(a.slice(0,2),b.slice(0,2));assert.ok(+a[2]<=2);assert.ok(+b[2]>=8);
  assert.equal(training[i].split(",").at(-1),+a[1]>=1.5?"1":"0");
 }
});
