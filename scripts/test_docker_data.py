import unittest
import sqlite3
from prepare_docker_data import rebase, rebase_registry


class DockerDataTests(unittest.TestCase):
    def test_profile_and_separate_key_manifest_move_together(self):
        with sqlite3.connect(":memory:") as db:
            db.execute("CREATE TABLE accounts(id TEXT, profile_path TEXT)")
            db.execute("CREATE TABLE account_settings(account_id TEXT, keys_file TEXT)")
            db.execute("INSERT INTO accounts VALUES('synthetic', '/home/user/native/accounts/a/profile')")
            db.execute("INSERT INTO account_settings VALUES('synthetic', '/home/user/native/accounts/a/keys.json')")
            rebase_registry(db, "/home/user/native")
            self.assertEqual(db.execute("SELECT profile_path FROM accounts").fetchone()[0], "/data/accounts/a/profile")
            self.assertEqual(db.execute("SELECT keys_file FROM account_settings").fetchone()[0], "/data/accounts/a/keys.json")

    def test_only_control_paths_rebased_no_chat_rewrite(self):
        old = "/home/user/native"
        value = {"path": old + "/admin/file", "request": old + "/private original words",
                 "nested": [{"token_file": old + "/api-token"}], "roots": [old + "/notes"],
                 "source_url": "https://example.test/"}
        result = rebase(value, old)
        self.assertEqual(result["path"], "/data/admin/file")
        self.assertEqual(result["request"], value["request"])
        self.assertEqual(result["nested"][0]["token_file"], "/data/api-token")
        self.assertEqual(result["roots"], ["/data/notes"])
        self.assertEqual(value["path"], old + "/admin/file")

    def test_prefix_siblings_and_non_paths_unchanged(self):
        value = {"path": "/home/user/native-other/file", "text": "/home/user/native/a"}
        self.assertEqual(rebase(value, "/home/user/native"), value)


if __name__ == "__main__":
    unittest.main()
