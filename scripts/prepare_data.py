"""Create a PII-free motor-claim demo extract from the Kaggle CSVs."""

import csv
import json
import sys
from pathlib import Path


def rows(path):
    with path.open(newline="", encoding="utf-8-sig") as handle:
        yield from csv.DictReader(handle)


def main(source_dir: Path, output: Path):
    agents = sorted({row["AGENT_ID"] for row in rows(source_dir / "employee_data.csv")})
    vendors = sorted({row["VENDOR_ID"] for row in rows(source_dir / "vendor_data.csv")})
    fields = {
        "TRANSACTION_ID": "id",
        "TXN_DATE_TIME": "received_at",
        "POLICY_EFF_DT": "policy_effective",
        "LOSS_DT": "loss_date",
        "REPORT_DT": "report_date",
        "CLAIM_AMOUNT": "amount",
        "INCIDENT_SEVERITY": "severity",
        "AUTHORITY_CONTACTED": "authority_contacted",
        "ANY_INJURY": "any_injury",
        "POLICE_REPORT_AVAILABLE": "police_report_available",
        "AGENT_ID": "agent_id",
        "VENDOR_ID": "vendor_id",
    }
    claims = []
    for row in rows(source_dir / "insurance_data.csv"):
        if row["INSURANCE_TYPE"] != "Motor":
            continue
        claim = {target: row[source] for source, target in fields.items()}
        claim["amount"] = int(claim["amount"])
        claim["any_injury"] = claim["any_injury"] == "1"
        claim["police_report_available"] = claim["police_report_available"] == "1"
        claims.append(claim)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        json.dumps({"claims": claims, "agents": agents, "vendors": vendors}, separators=(",", ":")),
        encoding="utf-8",
    )
    print(f"Wrote {len(claims)} motor claims to {output}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("Usage: python3 scripts/prepare_data.py SOURCE_DIR OUTPUT_JSON")
    main(Path(sys.argv[1]), Path(sys.argv[2]))
