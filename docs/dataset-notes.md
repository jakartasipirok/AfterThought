# Dataset discovery example

[Insurance Claims Fraud Data](https://www.kaggle.com/datasets/mastmustu/insurance-claims-fraud-data), inspected 7 November 2026. The public API reports version 2, last updated 28 December 2021, CC0.

| File | Rows | Observed role |
| --- | ---: | --- |
| `insurance_data.csv` | 10,000 | Claims/transactions |
| `employee_data.csv` | 1,200 | Agent/adjuster master data |
| `vendor_data.csv` | 600 | Vendor master data |

`AGENT_ID` and `VENDOR_ID` are candidate joins; uniqueness, missing matches, and cardinality require checks. The data includes several insurance types, not only motor insurance.

`CLAIM_STATUS` contains `A` (9,497) and `D` (503). The inspected schema and dataset description do not establish these as verified fraud labels. Predicting the majority status yields 94.97% status accuracy without demonstrating useful fraud detection. Claim fraud, employee fraud, and employee/vendor collusion are separate possible tasks suggested by the dataset card.

The schema includes direct identifiers and account fields. Profile locally and send aggregate summaries to the model. No downloaded records are committed or preloaded into agent memory. The URL is an example input, not a seeded business workflow.

A task should establish target semantics, decision-time feature availability, temporal/entity-disjoint splits, class imbalance, precision/recall, false-rejection costs, and escalation capacity. Generated candidates must be evaluated against an incumbent; a separate final audit set should remain outside repeated optimization.
