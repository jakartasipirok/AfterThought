import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { evaluateClaim, reconcile } from "../frontend/src/engine.mjs";

const data = JSON.parse(fs.readFileSync(new URL("../frontend/data/motor_claims.json", import.meta.url), "utf8"));
const refs = { agents: new Set(data.agents), vendors: new Set(data.vendors) };
const base = data.claims.find((claim) => claim.police_report_available && claim.severity === "Minor Loss" && claim.amount < 8000);

test("every source motor claim receives a deterministic route", () => {
  const counts = { Continue: 0, Review: 0, Hold: 0 };
  for (const claim of data.claims) counts[evaluateClaim(claim, refs).outcome]++;
  assert.deepEqual(counts, { Continue: 497, Review: 1077, Hold: 0 });
});

test("missing report is a review flag, not a fraud label", () => {
  const claim = { ...base, police_report_available: false };
  const result = evaluateClaim(claim, refs);
  assert.equal(result.outcome, "Review");
  assert.equal(result.route, "Document follow-up");
  assert.ok(result.business.some((flag) => flag.code === "MISSING_POLICE_REPORT"));
  assert.equal(reconcile(result, null).label, "Awaiting investigation");
});

test("complex claims go to an adjuster, not an automatic fraud referral", () => {
  const result = evaluateClaim({ ...base, severity: "Total Loss" }, refs);
  assert.equal(result.outcome, "Review");
  assert.equal(result.route, "Adjuster review");
});

test("an unavailable reference takes precedence and holds the claim", () => {
  const result = evaluateClaim({ ...base, police_report_available: false }, refs, { referenceOutage: true });
  assert.equal(result.outcome, "Hold");
  assert.equal(result.business.length, 0);
  assert.equal(reconcile(result, null).label, "Blocked");
});

test("an unassigned vendor is allowed but an unknown populated vendor holds", () => {
  assert.equal(evaluateClaim({ ...base, vendor_id: "" }, refs).outcome, "Continue");
  assert.equal(evaluateClaim({ ...base, vendor_id: "VNDR_UNKNOWN" }, refs).outcome, "Hold");
});

test("impossible calendar dates cannot pass screening", () => {
  const result = evaluateClaim({ ...base, loss_date: "2021-02-30" }, refs);
  assert.equal(result.outcome, "Hold");
  assert.ok(result.screening.some((flag) => flag.code === "INVALID_DATE"));
});

test("source status cannot influence screening and investigator decision controls reconciliation", () => {
  const claim = { ...base, police_report_available: false };
  const first = evaluateClaim({ ...claim, CLAIM_STATUS: "A" }, refs);
  const second = evaluateClaim({ ...claim, CLAIM_STATUS: "D" }, refs);
  assert.deepEqual(first, second);
  assert.equal(reconcile(first, { decision: "clear" }).label, "Ready for next stage");
  assert.equal(reconcile(first, { decision: "request" }).label, "Pending evidence");
  assert.equal(reconcile(first, { decision: "escalate" }).label, "Escalated");
});
