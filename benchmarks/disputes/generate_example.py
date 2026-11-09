"""Generate a reproducible, explicitly synthetic dispute triage example."""

import argparse
import csv
import json
import random
from pathlib import Path


FIELDS = [
    "case_id", "payment_amount", "refund_amount", "refund_status",
    "dispute_status", "hours_to_deadline", "evidence_complete",
    "order_found", "refund_count", "review_required",
]

POLICY = (
    "Mark review_required=1 when the order is missing; total refunds exceed the "
    "payment; an open dispute exists after a completed full refund; an open "
    "dispute has less than 48 hours remaining; or an open dispute lacks its "
    "required evidence. Otherwise mark 0. This is a fictional triage policy, "
    "not a fraud finding or a real merchant policy."
)


def make_case(rng, index):
    payment = rng.choice([25, 40, 75, 120, 240, 600])
    refund_status = rng.choices(["none", "pending", "succeeded"], [5, 2, 3])[0]
    refund_count = 0 if refund_status == "none" else rng.choices([1, 2], [9, 1])[0]
    refund = 0 if refund_count == 0 else rng.choice([round(payment / 2, 2), payment, payment * 1.2])
    dispute_status = rng.choices(["none", "open", "closed"], [5, 4, 1])[0]
    deadline = rng.choice([6, 24, 36, 72, 120]) if dispute_status == "open" else 0
    evidence = rng.choices([0, 1], [3, 7])[0]
    order_found = rng.choices([0, 1], [1, 19])[0]
    review = (
        not order_found
        or refund > payment
        or (dispute_status == "open" and refund_status == "succeeded" and refund >= payment)
        or (dispute_status == "open" and deadline < 48)
        or (dispute_status == "open" and not evidence)
    )
    return {
        "case_id": f"SYN-{index:06d}", "payment_amount": payment,
        "refund_amount": refund, "refund_status": refund_status,
        "dispute_status": dispute_status, "hours_to_deadline": deadline,
        "evidence_complete": evidence, "order_found": order_found,
        "refund_count": refund_count, "review_required": int(review),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out-dir", type=Path, default=Path("benchmarks/disputes/example"))
    parser.add_argument("--train", type=int, default=800)
    parser.add_argument("--test", type=int, default=200)
    args = parser.parse_args()
    if args.train < 50 or args.test < 1:
        parser.error("need at least 50 training and one test case")
    args.out_dir.mkdir(parents=True, exist_ok=True)
    rng = random.Random(20261107)
    cases = [make_case(rng, i) for i in range(args.train + args.test)]
    for name, rows in (("train", cases[:args.train]), ("test", cases[args.train:])):
        with (args.out_dir / f"{name}.csv").open("w", newline="") as output:
            writer = csv.DictWriter(output, fieldnames=FIELDS)
            writer.writeheader()
            writer.writerows(rows)
    (args.out_dir / "policy.json").write_text(json.dumps({
        "synthetic": True,
        "label_provenance": "Deterministic fictional policy in generate_example.py",
        "label_column": "review_required", "id_column": "case_id",
        "policy": POLICY,
        "train_rows": args.train, "test_rows": args.test,
    }, indent=2) + "\n")
    print(f"Wrote {args.train} synthetic training and {args.test} synthetic test cases to {args.out_dir}")


if __name__ == "__main__":
    main()
