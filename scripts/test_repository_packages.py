"""Synthetic index checks; never reads local credentials or changes Git."""
import hashlib
import json
import unittest

from check_repository import package_findings


class RepositoryPackageTests(unittest.TestCase):
    def package(self, data=b"line\n"):
        return {"plugins/synthetic/plugin.json":json.dumps({"files":{"runtime/main.py":hashlib.sha256(data).hexdigest()}}).encode(),
                "plugins/synthetic/runtime/main.py":data}

    def test_cloned_bytes_pass(self):
        self.assertEqual(package_findings(self.package()),[])

    def test_windows_digest_cannot_describe_lf_index(self):
        index=self.package(b"line\r\n")
        index["plugins/synthetic/runtime/main.py"]=b"line\n"
        self.assertEqual(package_findings(index)[0]["reason"],"indexed_package_digest_mismatch")

    def test_missing_and_extra_files_are_not_hidden(self):
        index=self.package();del index["plugins/synthetic/runtime/main.py"]
        self.assertEqual(package_findings(index)[0]["reason"],"indexed_package_digest_mismatch")
        index=self.package();index["plugins/synthetic/runtime/extra.py"]=b"extra"
        self.assertEqual(package_findings(index)[0]["reason"],"unindexed_package_member")

    def test_malformed_or_traversing_manifest_fails(self):
        for document in ({"files":{"../outside":"a"*64}},{"files":[]},None):
            index={"plugins/synthetic/plugin.json":json.dumps(document).encode()}
            self.assertTrue(package_findings(index))


if __name__ == "__main__":unittest.main()
