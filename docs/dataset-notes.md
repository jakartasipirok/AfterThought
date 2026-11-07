# Datasets and evaluation

## Supplied labelled claims

The repeated-learning demo prepares three operator-supplied local files. Raw CSVs and the generated bundle stay outside Git.

| Source | Supplied rows | Label distribution |
| --- | ---: | --- |
| `fraud_oracle.csv` | 15,420 | 923 fraud and 14,497 legitimate |
| `new_fraud_only.csv` | 149 | Fraud only |
| `novel_pattern_fraud_only.csv` | 100 | Fraud only |

The target is `FraudFound_P`, with `1` as fraud and `0` as legitimate. Provenance is operator supplied. The challenge filenames identify data groups and do not independently establish a new scam.

Preparation excludes `PolicyNumber`, `RepNumber`, and `AddressChange_Claim`, removes duplicate feature vectors across files, and drops conflicting-label groups. It pairs challenge fraud with legitimate historical controls. With these files, one duplicate is removed, producing a 400-row baseline, additions of 600/196/140 rows, and a separate 560-row benchmark. See [preparation and splits](demo-learning-rounds.md#data-preparation).

The initial 400 rows produce 240 training, 80 development, and 80 audit cases. Additional rounds merge only training/development partitions. **Original audit accuracy** always refers to those original 80 held-out cases. **Benchmark accuracy** uses the same reserved 560 cases, containing 280 fraud and 280 legitimate labels. The demo's balanced sampling differs from the historical file's approximately 6% fraud rate.

Every version is evaluated from its actual predictions. Repeated measurement supports comparison, and a fresh final dataset is needed to assess generalization after selecting a version. Entity/time-based splitting is still an operator preparation concern.

## Earlier Kaggle discovery and browser replay

[Insurance Claims Fraud Data](https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data), inspected 7 November 2026. The public API reports version 2, last updated 28 December 2021, CC0.

| File | Rows | Observed role |
| --- | ---: | --- |
| `insurance_data.csv` | 10,000 | Claims/transactions |
| `employee_data.csv` | 1,200 | Agent/adjuster master data |
| `vendor_data.csv` | 600 | Vendor master data |

`AGENT_ID` and `VENDOR_ID` are candidate joins; uniqueness, missing matches, and cardinality require checks. The data includes several insurance types, not only motor insurance.

`CLAIM_STATUS` contains `A` (9,497) and `D` (503). The inspected schema and dataset description do not establish these as verified fraud labels. Predicting the majority status yields 94.97% status accuracy without demonstrating useful fraud detection. Claim fraud, employee fraud, and employee/vendor collusion are separate possible tasks suggested by the dataset card.

The schema includes direct identifiers and account fields. Profile locally and send aggregate summaries to the model. Raw source CSVs stay local. The repository includes a curated motor-claim extract for the [illustrative browser replay](claims-replay.md). Live authoring starts from its supplied task and permitted data.

## Synthetic policy demo

The policy walkthrough generates 1,000 labelled claims using repair-estimate ratio. A later batch changes evidence reuse from 0–2 to 8–12 to exercise numerical-range review. Its fictional [company policy](demo-policy.md) supports a cited investigator-procedure amendment. Changes to investigation reasons may leave classification metrics equal. Read the displayed before/after results when describing the outcome.

## Custom CSV intake

Supply a headered CSV, an explicit binary target and positive label, permitted features, label meaning/provenance, and evaluation thresholds. Intake requires at least 50 rows and 10 per class. The limits are 64 MiB, 100,000 rows, 128 source columns, and 64 selected features. The three-file preparation script specifically expects `FraudFound_P`; use the [manual workspace](demo.md) for other compatible schemas.

A task should establish target semantics, decision-time feature availability, temporal/entity-disjoint splits, class imbalance, precision/recall, false-rejection costs, and escalation capacity. Generated candidates must be evaluated against an incumbent; a separate final audit set should remain outside repeated optimization.
