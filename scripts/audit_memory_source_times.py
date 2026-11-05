"""Read-only source time audit. Emits identities and counts, never chat text.

Compare retained incoming activity with SQLite originals before repairing data.
This detects disagreement; it does not decide which timestamp is authoritative.
"""
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import sqlite3


def timestamp(value):
    if not value:
        return None
    try:
        value = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return value if value.tzinfo else None
    except (ValueError, TypeError):
        return None


def audit(database, activity):
    rows = json.loads(Path(activity).read_text(encoding="utf-8"))
    observations = {}
    for row in rows:
        if row.get("event") != "message_received" or not row.get("message_id"):
            continue
        key = (row.get("thread_id"), row["message_id"])
        if timestamp(row.get("message_time")):
            observations.setdefault(key, []).append(row)
    report = {"version": 1, "read_only": True, "content_included": False,
              "events_checked": 0, "matched_activity": 0, "disagreements": [],
              "coverage": "retained_activity_only", "repair_applied": False}
    uri = Path(database).resolve().as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True) as conn:
        scopes = {scope: json.loads(payload).get("thread")
                  for scope, payload in conn.execute("SELECT id,payload FROM scopes")}
        for event, scope, trace, sender, at, revision in conn.execute(
                "SELECT id,scope,trace,sender,at,revision FROM events WHERE role='user'"):
            report["events_checked"] += 1
            candidates = observations.get((scopes.get(scope), trace), [])
            if not candidates:
                continue
            report["matched_activity"] += 1
            original = timestamp(at)
            for row in candidates:
                seen = timestamp(row.get("message_time"))
                changed_sender = bool(sender and row.get("sender") and sender != row["sender"])
                delta = abs((seen-original).total_seconds()) if seen and original else None
                if not changed_sender and delta is not None and delta <= 1:
                    continue
                report["disagreements"].append({
                    "event_id": event, "scope": scope, "revision": revision,
                    "message_identity_sha256": hashlib.sha256(trace.encode()).hexdigest(),
                    "stored_time": at, "observed_message_time": row.get("message_time"),
                    "difference_seconds": delta, "sender_disagrees": changed_sender,
                    "resolution": "requires_authoritative_source_verification"})
    report["disagreement_count"] = len(report["disagreements"])
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--activity", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    report = audit(args.database, args.activity)
    encoded = json.dumps(report, ensure_ascii=False, indent=2)
    if args.output:
        with args.output.open("x", encoding="utf-8") as stream:
            stream.write(encoded + "\n")
        print(json.dumps({key: value for key, value in report.items()
                          if key != "disagreements"}, ensure_ascii=False))
    else:
        print(encoded)


if __name__ == "__main__":
    main()
