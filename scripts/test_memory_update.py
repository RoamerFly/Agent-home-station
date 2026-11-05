import importlib.util
from contextlib import closing
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("memory_update", Path(__file__).with_name("update_local_memory.py"))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class MemoryBackupTests(unittest.TestCase):
    def test_consistent_wal_backup_preserves_metadata_and_skips_immutable_media(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "admin"
            source.mkdir()
            (source / "config.json").write_text('{"fixture":true}')
            media = source / "media-assets"
            media.mkdir()
            (media / "immutable.bin").write_bytes(b"synthetic media")
            db = sqlite3.connect(source / "memory.sqlite")
            try:
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("CREATE TABLE fixture(value TEXT)")
                db.execute("INSERT INTO fixture VALUES('committed')")
                db.commit()
                target = Path(tmp) / "backup"
                update.metadata_backup(source, target)
                with closing(sqlite3.connect(target / "memory.sqlite")) as copy:
                    self.assertEqual(copy.execute("SELECT value FROM fixture").fetchone()[0], "committed")
                self.assertEqual((target / "config.json").read_bytes(), (source / "config.json").read_bytes())
                self.assertFalse((target / "media-assets").exists())
                self.assertFalse((target / "memory.sqlite-wal").exists())
            finally:
                db.close()


if __name__ == "__main__":
    unittest.main()
