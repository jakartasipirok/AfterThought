export const RULESET = "Motor triage v1.0";

function isValidDate(value) {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(date.valueOf()) && date.toISOString().slice(0, 10) === value;
}

export function evaluateClaim(claim, references, options = {}) {
  const screening = [];
  const business = [];
  const required = ["id", "received_at", "policy_effective", "loss_date", "report_date", "amount", "severity", "authority_contacted", "agent_id"];
  for (const key of required) {
    if (claim[key] === null || claim[key] === undefined || claim[key] === "") {
      screening.push({ code: "MISSING_FIELD", text: `Required field missing: ${key}`, evidence: key });
    }
  }
  for (const key of ["policy_effective", "loss_date", "report_date"]) {
    if (claim[key] && !isValidDate(claim[key])) {
      screening.push({ code: "INVALID_DATE", text: `Invalid ${key}`, evidence: String(claim[key]) });
    }
  }
  const receivedDate = claim.received_at?.slice(0, 10);
  if (receivedDate && !isValidDate(receivedDate)) {
    screening.push({ code: "INVALID_DATE", text: "Invalid received date", evidence: String(claim.received_at) });
  }
  if (!Number.isFinite(Number(claim.amount)) || Number(claim.amount) <= 0) {
    screening.push({ code: "INVALID_AMOUNT", text: "Claim amount must be positive", evidence: String(claim.amount) });
  }
  if (claim.policy_effective && claim.loss_date && claim.report_date && receivedDate &&
      [claim.policy_effective, claim.loss_date, claim.report_date, receivedDate].every(isValidDate)) {
    if (!(claim.policy_effective <= claim.loss_date && claim.loss_date <= claim.report_date && claim.report_date <= receivedDate)) {
      screening.push({ code: "DATE_ORDER", text: "Policy, loss, report, and received dates are inconsistent", evidence: `${claim.policy_effective} → ${claim.loss_date} → ${claim.report_date} → ${receivedDate}` });
    }
  }
  if (options.referenceOutage) {
    screening.push({ code: "REFERENCE_UNAVAILABLE", text: "Agent reference lookup unavailable (simulated)", evidence: "Reference service outage" });
  } else {
    if (claim.agent_id && !references.agents.has(claim.agent_id)) {
      screening.push({ code: "UNKNOWN_AGENT", text: "Agent ID not found in reference data", evidence: claim.agent_id });
    }
    if (claim.vendor_id && !references.vendors.has(claim.vendor_id)) {
      screening.push({ code: "UNKNOWN_VENDOR", text: "Vendor ID not found in reference data", evidence: claim.vendor_id });
    }
  }
  if (screening.length) {
    return { outcome: "Hold", route: "Data operations", screening, business, ruleset: RULESET };
  }

  if (!claim.police_report_available) {
    business.push({ code: "MISSING_POLICE_REPORT", text: "Police report missing; document review required", evidence: `Report: absent · Severity: ${claim.severity} · Injury: ${claim.any_injury ? "yes" : "no"} · Authority: ${claim.authority_contacted}` });
  }
  // Fictional demo thresholds. These are not learned from the source dataset.
  if (Number(claim.amount) >= 8000) {
    business.push({ code: "HIGH_AMOUNT", text: "Claim meets the demo high amount threshold", evidence: `${Number(claim.amount).toLocaleString()} ≥ 8,000` });
  }
  if (claim.severity === "Total Loss") {
    business.push({ code: "TOTAL_LOSS", text: "Total loss requires investigator review", evidence: claim.severity });
  }
  if (business.length) {
    const complexClaim = business.some((flag) => flag.code === "HIGH_AMOUNT" || flag.code === "TOTAL_LOSS");
    return { outcome: "Review", route: complexClaim ? "Adjuster review" : "Document follow-up", screening, business, ruleset: RULESET };
  }
  return { outcome: "Continue", route: "Downstream processing", screening, business, ruleset: RULESET };
}

export function reconcile(evaluation, investigation) {
  if (evaluation.outcome === "Hold") return { label: "Blocked", detail: "Resolve the screening prerequisite and run checks again." };
  if (evaluation.outcome === "Continue") return { label: "Ready for next stage", detail: "Checks passed. This is not a payment approval." };
  if (!investigation) return { label: "Awaiting investigation", detail: "A claims reviewer must record a decision." };
  if (investigation.decision === "clear") return { label: "Ready for next stage", detail: "Claims reviewer cleared the concern. This is not a payment approval." };
  if (investigation.decision === "request") return { label: "Pending evidence", detail: "Additional documents or facts are required." };
  return { label: "Escalated", detail: "Potential fraud concern sent for specialist handling." };
}
