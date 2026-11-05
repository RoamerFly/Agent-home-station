"""Create a private, read-only fingerprint for a full companion memory DB.

This is for the isolated migration rehearsal in the long-growth plan. Copy a
stopped account's ``memory.sqlite`` plus any ``-wal`` and ``-shm`` sidecars to
a private temporary directory, snapshot it, bind or restart, then snapshot a
new stopped copy and compare the two JSON files. The command never prints
message bodies, source IDs, account IDs, or SQLite values.

Example:
  python scripts/check_identity_memory.py --database C:/Temp/before/memory.sqlite --output C:/Temp/before/snapshot.json
  python scripts/check_identity_memory.py --database C:/Temp/after/memory.sqlite --compare C:/Temp/before/snapshot.json
"""

import argparse
import hashlib
import json
from pathlib import Path
import sqlite3
import sys
from typing import Any, NoReturn


EXPECTED_SCHEMA = "2"
REQUIRED_TABLES = {
    "meta", "scopes", "events", "observations", "event_revisions", "event_terms", "jobs",
    "memories", "memory_sources", "memory_terms", "memory_revisions", "embeddings",
    "maintenance_usage", "memory_blocks", "rebuild_requests", "search_documents",
    "source_conflicts", "context_metrics",
}


def fail(message: str) -> NoReturn:
    print(json.dumps({"ok": False, "error": message}, ensure_ascii=False), file=sys.stderr)
    raise SystemExit(2)


def canonical_digest(value: Any) -> str:
    raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def normalized(value: Any) -> Any:
    # Keep the SQLite storage class in the digest. Without a type tag, a REAL
    # formatted as "1" could collide with TEXT "1".
    if value is None:
        return ["null", None]
    if isinstance(value, bytes):
        return ["blob", len(value), hashlib.sha256(value).hexdigest()]
    if isinstance(value, float):
        return ["real", value.hex()]
    if isinstance(value, int):
        return ["integer", value]
    return ["text", str(value)]


def quote_identifier(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def table_snapshot(db: sqlite3.Connection, table: str) -> dict[str, Any]:
    cursor = db.execute("SELECT * FROM " + quote_identifier(table))
    # Hash each typed row first, then sort its fixed-size digest. This is stable
    # for nullable/non-unique keys and for WITHOUT ROWID tables.
    row_hashes: list[bytes] = []
    for row in cursor:
        encoded = json.dumps([normalized(value) for value in row], ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")
        row_hashes.append(hashlib.sha256(encoded).digest())
    row_hashes.sort()
    h = hashlib.sha256()
    for row_hash in row_hashes:
        h.update(row_hash)
    return {"columns": len(cursor.description), "rows": len(row_hashes), "sha256": h.hexdigest()}


def schema_snapshot(db: sqlite3.Connection) -> tuple[dict[str, dict[str, Any]], str]:
    result: dict[str, dict[str, Any]] = {}
    schema_records = []
    for kind, name, owner, ddl in db.execute("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*' ORDER BY type,name"):
        schema_records.append([str(kind), str(name), str(owner), hashlib.sha256((ddl or "").encode("utf-8")).hexdigest()])
        if kind != "table":
            continue
        table = str(name)
        result[table] = {"ddl_sha256": hashlib.sha256((ddl or "").encode("utf-8")).hexdigest(), **table_snapshot(db, table)}
    return result, canonical_digest(schema_records)


def prohibited_output(database: Path, output: Path) -> bool:
    protected = {database, database.with_name(database.name + "-wal"), database.with_name(database.name + "-shm")}
    return output in protected or output.exists() or output.is_symlink()


def main() -> None:
    parser = argparse.ArgumentParser(description="Read-only full-database companion memory rehearsal snapshot")
    parser.add_argument("--database", required=True, help="private stopped-copy memory.sqlite path")
    parser.add_argument("--output", help="write JSON snapshot only to a new private path")
    parser.add_argument("--compare", help="compare against a prior snapshot JSON")
    args = parser.parse_args()

    raw_database = Path(args.database)
    if raw_database.is_symlink():
        fail("database_not_regular_file")
    database = raw_database.resolve()
    if not database.is_file() or database.is_symlink():
        fail("database_not_regular_file")
    db = None
    try:
        # as_uri preserves Windows drive and space handling, unlike string URI
        # concatenation. mode=ro and query_only keep this process read-only.
        db = sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)
        db.execute("PRAGMA query_only=ON")
        db.execute("BEGIN")
        schema = db.execute("SELECT value FROM meta WHERE key='schema'").fetchone()
        if not schema or schema[0] != EXPECTED_SCHEMA:
            fail("unsupported_memory_schema")
        tables, schema_sha256 = schema_snapshot(db)
        if REQUIRED_TABLES.difference(tables):
            fail("memory_schema_missing_required_tables")
        db.execute("COMMIT")
    except sqlite3.Error:
        fail("database_unreadable_or_incomplete")
    finally:
        try:
            if db is not None:
                db.close()
        except sqlite3.Error:
            pass

    result = {
        "format": 2,
        "sqlite_schema": EXPECTED_SCHEMA,
        "table_count": len(tables),
        "wal_sidecar_present": database.with_name(database.name + "-wal").exists(),
        "sqlite_schema_sha256": schema_sha256,
        "tables": tables,
    }
    comparison = {key: value for key, value in result.items() if key != "wal_sidecar_present"}
    result["snapshot_sha256"] = canonical_digest(comparison)
    if args.compare:
        try:
            prior = json.loads(Path(args.compare).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            fail("comparison_snapshot_unreadable")
        if not isinstance(prior, dict) or prior.get("format") != result["format"] or not prior.get("snapshot_sha256"):
            fail("invalid_comparison_snapshot")
        result["matches_comparison"] = prior["snapshot_sha256"] == result["snapshot_sha256"]
    rendered = json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        raw_output = Path(args.output)
        if raw_output.is_symlink():
            fail("unsafe_or_existing_output_path")
        output = raw_output.resolve()
        if prohibited_output(database, output):
            fail("unsafe_or_existing_output_path")
        try:
            output.parent.mkdir(parents=True, exist_ok=True)
            with output.open("x", encoding="utf-8", newline="\n") as handle:
                handle.write(rendered)
        except OSError:
            fail("snapshot_output_failed")
    print(rendered, end="")
    if args.compare and not result["matches_comparison"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
