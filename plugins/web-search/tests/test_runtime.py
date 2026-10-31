"""Offline parser/pagination regression. No real HTTP or model calls."""
import base64
import importlib.util
import io
import json
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

    def test_deepseek_forwards_explicit_count_without_public_fallback(self):
        calls = []
        def route(name, body, maximum, timeout):
            calls.append((name, body, maximum, timeout))
            return {"results": [{"title": "Official", "url": "https://example.test/official", "summary": "synthetic"}]}
        result = plugin.search({"query": "music", "count": 7, "limit": 1}, lambda *_: self.fail("public fallback"), route, {"search_backend": "deepseek"})
        self.assertEqual(result["backend"], "deepseek")
        self.assertEqual(result["effective_provider_count"], 7)
        self.assertEqual(calls, [("deepseek-search", {"query": "music", "count": 7, "need_content": False}, 131072, 18)])
        with self.assertRaisesRegex(ValueError, "invalid_count"):
            plugin.search({"query": "music", "count": 0}, lambda *_: self.fail("public fallback"), route, {"search_backend": "deepseek"})
        self.assertEqual(len(calls), 1)

    def test_deepseek_forwards_long_unicode_query_unchanged(self):
        query = "鲸鱼" * 130
        captured = []
        def route(name, body, maximum, timeout):
            captured.append((name, body, maximum, timeout))
            return {"results": []}
        result = plugin.search({"query": query}, lambda *_: self.fail("public fallback"), route, {"search_backend": "deepseek"})
        self.assertTrue(result["ok"])
        self.assertEqual(captured[0][1], {"query": query, "count": 6, "need_content": False})

    def test_deepseek_wire_uses_route_and_configuration_only(self):
        context = {key: "synthetic" for key in ("account", "thread", "requester", "purpose", "request_key")}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": {"search_backend": "deepseek"}}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "web.search", "arguments": {"query": "music"}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "web-route-1", "result": {"results": [{"title": "Official", "url": "https://example.test/official", "summary": "synthetic"}]}},
        ]
        output = io.StringIO()
        plugin.Wire(io.StringIO("".join(json.dumps(message) + "\n" for message in messages)), output).serve()
        replies = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(replies[1]["method"], "wechat/http_route")
        self.assertEqual(replies[1]["params"]["route"], "deepseek-search")
        self.assertEqual(replies[1]["params"]["body"], {"query": "music", "count": 6, "need_content": False})
        self.assertEqual(replies[2]["result"]["backend"], "deepseek")

    def test_no_fake_success_or_file_read(self):
        for url in ("file:///secrets", "https://user:secret@example.test/", "ftp://example.test/"):
            with self.assertRaises(ValueError):
                plugin.fetch({"url": url}, lambda *args: self.fail("invalid URL reached host"))
        failed = plugin.fetch({"url": "https://example.test/page"}, lambda *args: {"ok": False, "error": "private_destination"})
        self.assertFalse(failed["ok"])
        self.assertEqual(failed["error"], "private_destination")


if __name__ == "__main__":
    unittest.main()
