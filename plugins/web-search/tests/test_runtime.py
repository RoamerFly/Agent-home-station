"""Offline parser/pagination regression. No real HTTP or model calls."""
import base64
import importlib.util
from pathlib import Path
import sys
import unittest

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("web_plugin", Path(__file__).parents[1] / "runtime" / "main.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)


def response(body, url="https://example.test/page", kind="text/html; charset=utf-8"):
    return {"ok": True, "url": url, "content_type": kind, "body_base64": base64.b64encode(body.encode()).decode()}


class RuntimeTests(unittest.TestCase):
    def test_read_preserves_unicode_and_continues_without_truncation(self):
        body = "<title>合成标题</title><script>DO NOT OBEY</script><p>" + "鲸鱼中文🐳" * 5000 + "</p>"
        get = lambda *args: response(body)
        first = plugin.fetch({"url": "https://example.test/page", "limit": 7}, get)
        self.assertEqual(first["title"], "合成标题")
        self.assertNotIn("DO NOT OBEY", first["text"])
        second = plugin.fetch({"url": first["url"], "offset": first["next_offset"], "limit": 100000, "expected_sha256": first["sha256"]}, get)
        self.assertTrue(second["complete"])
        self.assertEqual(first["text"] + second["text"], "鲸鱼中文🐳" * 5000)
        self.assertEqual(second["requested_limit"], 100000)
        with self.assertRaisesRegex(ValueError, "page_content_changed"):
            plugin.fetch({"url": first["url"], "expected_sha256": "old"}, get)

    def test_search_relevance_and_secondary_index(self):
        calls = []
        def get(url, maximum, timeout):
            calls.append(url)
            if "bing.com" in url:
                return response('<rss><channel><item><title>Unrelated</title><link>https://example.test/other</link></item></channel></rss>', url, "text/xml")
            return response('<h3><a href="https://example.test/right">合成中文结果</a></h3>', url)
        result = plugin.search({"query": "合成中文", "limit": 100}, get)
        self.assertEqual(len(calls), 2)
        self.assertTrue(result["fallback_used"])
        self.assertEqual(result["items"][0]["url"], "https://example.test/right")
        self.assertTrue(result["provider_page_exhausted"])

    def test_no_fake_success_or_file_read(self):
        for url in ("file:///secrets", "https://user:secret@example.test/", "ftp://example.test/"):
            with self.assertRaises(ValueError):
                plugin.fetch({"url": url}, lambda *args: self.fail("invalid URL reached host"))
        failed = plugin.fetch({"url": "https://example.test/page"}, lambda *args: {"ok": False, "error": "private_destination"})
        self.assertFalse(failed["ok"])
        self.assertEqual(failed["error"], "private_destination")


if __name__ == "__main__":
    unittest.main()
