# AfterThought

**Check the claim. Route exceptions. Close the loop.**

AfterThought is a hackathon prototype for motor-claim exception handling. It replays structured claim records through a deterministic, versioned workflow. Routine claims use **zero LLM calls**. The demo shows how a claim is screened, checked against business rules, routed, reviewed by a person when needed, and given a recorded next action.

## End-to-end overview

| Stage | What happens | Demo outcome |
| --- | --- | --- |
| 1. Screening | Validate required fields, positive amount, date order, and agent/vendor references. A vendor can be unassigned. | A failed prerequisite yields **Hold** and stops later checks. |
| 2. Automated Business Checking | Apply fictional demo rules for missing police report, amount at least $8,000, and total loss. | Each flag has a reason and claim evidence. |
| 3. Dynamic Routing | Send an unflagged claim onward; send a missing-report-only claim to document follow-up; send a high-value or total-loss claim to adjuster review. | **Continue** or **Review**, with the assigned route. |
| 4. Investigation | A claims reviewer records a note and chooses clear concern, request evidence, or escalate concern. | A human disposition; escalation is the only path toward specialist fraud handling. |
| 5. Reconciliation | Combine the route and disposition into the next workflow state. | Ready for next stage, awaiting investigation, pending evidence, escalated, or blocked. |

This last stage is **disposition reconciliation**: it makes the exception and its resolution visible in one trace. It is not financial reconciliation. A production insurer would also reconcile authorized settlements with reserves, payment transactions, recoveries, its financial systems, and claim-file evidence. This prototype has no payment or ledger data and never authorizes payment. See [Guidewire's claim financial transaction model](https://docs.guidewire.com/cloud/cc/202511/cloudapibf/cloudAPI/topics/112-CCFin/05-financial-calculations/c_financial_calculations.html) for an industry example.

### A concrete claim story

1. A motor claim arrives without a police report.
2. The demo flags the missing document and routes the claim to **Document follow-up**. The flag is not a fraud accusation.
3. In a real insurer, a reviewer would check the policy and evidence. The demo records the reviewer's note and choice to clear the concern, request more evidence, or escalate it.
4. Reconciliation records whether the claim can continue or still needs action. Payment approval remains outside this demo.

For a later rule-improvement demonstration, an **explicitly fictional insurer policy** could make a police report optional for a defined minor-loss, no-injury, no-police-contact case. The source contains 39 candidate motor records. That would change one flag count, not establish claim legitimacy or fraud accuracy. Any such rule should be tested and approved before release.

## What the replay shows

The unchanged source extract contains **1,574 motor claims**. Under the fictional v1 rules, 1,077 route to Review and 497 to Continue; none fail source-data screening. The missing-report flag appears on 461 claims. The UI can simulate a reference outage for one claim to show Hold and recovery. These are workflow counts, not fraud performance measures.

The review queue includes ordinary document follow-up and adjuster work. Specialist fraud handling requires a human escalation. A real insurer would use its own policy, coverage records, evidence requirements, investigation procedures, and jurisdiction-specific rules.

## Run the frontend

From the repository root:

```bash
python3 -m http.server 8000 -d frontend
```

Open [http://localhost:8000](http://localhost:8000). Click a claim to inspect its five-stage trace. On a Review claim, add an investigation note and choose a disposition. Use **Simulate reference outage** to inspect Hold and recovery. Demo decisions are stored only in your browser; **Reset demo decisions** clears them.

Run the workflow tests:

```bash
node --test tests/engine.test.mjs
```

## Repository layout

```text
AfterThought/
├── AGENTS.md                 # Coding-agent entry point
├── README.md                 # Product and workflow overview
├── frontend/
│   ├── index.html
│   ├── src/                  # UI and deterministic workflow engine
│   └── data/                 # PII-free motor claims and ID references
├── scripts/prepare_data.py  # Rebuild the frontend extract
└── tests/engine.test.mjs
```

## Data provenance and limits

`frontend/data/motor_claims.json` is a PII-free extract of the motor rows from Kaggle's [Insurance Claims Fraud Data](https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data/data), plus agent and vendor ID reference sets. The source `CLAIM_STATUS` field is excluded. The dataset has **no verified fraud label**, policy coverage master, payment ledger, or adjudicated investigation outcome. Do not use its historical status codes as fraud truth or claim a measured fraud reduction, precision, recall, or financial ROI.

To rebuild the extract from the original three CSVs:

```bash
python3 scripts/prepare_data.py /path/to/source-csv-directory frontend/data/motor_claims.json
```
