# Synthetic motor claims review policy

Fictional hackathon reference, version 1. Public guidance was reviewed on 7 November 2026.

## Use in the frontend

Choose **Policy review · synthetic claims** on `/graphs.html`. **Start demo** uploads the bundled [policy PDF](../frontend/assets/demo-company-policy.pdf) and generated data. The runtime pins the uploaded document for authoring and stores later amendments as new versions. This Markdown describes the reference policy; editing it alone does not replace the bundled PDF or an uploaded version.

For your own knowledge, use the technical workspace to upload a searchable PDF with Poppler installed. Upload limits are 8 MiB and 30 pages. Review proposals expose cited passages, observed signals, the proposed procedure, and alternative explanations. Enter your assessment, note, and reviewer identity, then choose **Approve policy amendment** or **Dismiss proposal** directly in the frontend.

In the guided demo, approval of an amendment leads the browser to request a serving-output-node revision. Review its source and evaluation, then separately click **Approve and activate**. See the [presenter walkthrough](demo-90-seconds.md#prepare-the-policy-walkthrough). Repeated learning with the supplied claims files has its own data-and-fit-node flow.

## Purpose and authority

This fictional AfterThought company policy supports a hackathon demonstration using synthetic claims. Human investigators decide claim outcomes and approve changes to this policy. Numerical screening signals initiate evidence review.

## 1. Intake and routing

Collect claim amount, coverage dates, incident description, repair estimate and available supporting documents. Check completeness and record the evidence source. Route inconsistent or incomplete submissions to an investigator. Record the reason for escalation and the evidence required to resolve it.

## 2. Repair and incident evidence

Compare the repair estimate with the stated incident and the vehicle damage described in supporting records. Seek an independent estimate when the amounts or documents appear inconsistent. Retain the original documents and document each verification step.

## 3. Pattern review

Investigators may compare related submissions for repeated circumstances, documentation discrepancies and unusual repair costs. A numerical departure from historical data requires assessment of benign explanations, data quality and operational changes. Record the working hypothesis and the additional evidence needed.

## 4. Decisions and amendments

A human records whether the observed pattern suggests a possible scam, a benign change, or further investigation. A proposed policy amendment must cite the current policy, reference the observed evidence and explain uncertainty. Approval creates a new policy version. A graph revision requires measured evaluation and a separate activation decision.

## 5. Public guidance

NICB describes caused, staged and fabricated accidents. Investigators should consider incident consistency and supporting documentation when assessing suspicious submissions. Public examples provide investigation context for this fictional procedure.

## 6. Emerging documentation risks

Allianz reports cases involving altered vehicle photographs and fabricated repair evidence. Such reports motivate questions about source authenticity. This demo processes numeric CSV features. Verifying a photograph requires the underlying image and an appropriate investigation.

## 7. Dataset alignment

The referenced Kaggle insurance claims dataset includes claims, employee and vendor tables across multiple insurance types. This policy focuses the demo on motor-claim investigation. Synthetic features such as evidence_reuse_count and repair_estimate_ratio illustrate additional evidence that an insurer could collect. The generated label is a demonstration target.

## 8. Version 1 scope

This version establishes general investigation and amendment procedures. New operational checks can be added after an investigator reviews supporting observations. Every addition must include an action, its evidence requirements and the responsible human reviewer.

## Public sources

National Insurance Crime Bureau - Staged & Caused Accidents
https://www.nicb.org/prevent-fraud-theft/staged-and-caused-accidents

Allianz UK - Posting car pics fuels fraud
https://www.allianz.co.uk/news-and-insight/news/posting-car-photos-fuels-fraud.html

Texas Department of Insurance - Types of fraud
https://www.tdi.texas.gov/fraud/types-of-fraud.html
