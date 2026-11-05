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
    def test_site_refusal_falls_back_to_matching_indexed_bv_only(self):
        def get(url, *_):
            if url.startswith("https://api.bilibili.com/"):
                return {"ok": False, "error": "http_status"}
            self.assertIn("site%3Abilibili.com%2Fvideo", url)
            rss = "<rss><channel>" + "".join(
                f"<item><title>{title}</title><link>{link}</link></item>" for title, link in [
                    ("歌手 另一首", "https://www.bilibili.com/video/BV0000000001/"),
                    ("歌手 歌名", "https://example.test/other"),
                    ("歌手 歌名", "https://www.bilibili.com/video/BV0000000002/")]) + "</channel></rss>"
            return response(rss, url, "text/xml")
        r = plugin.search({"source": "bilibili", "query": "歌手 歌名 原唱"}, get)
        self.assertTrue(r["fallback_used"])
        self.assertEqual([i["url"] for i in r["items"]], ["https://www.bilibili.com/video/BV0000000002/"])

    def test_bilibili_search_uses_third_public_index_after_two_transport_failures(self):
        calls = []
        def get(url, *_):
            calls.append(url)
            if "api.bilibili.com" in url or "bing.com" in url or "so.com" in url:
                return {"ok": False, "error": "http_status"}
            self.assertIn("search.brave.com/search", url)
            return response('<h3><a href="https://www.bilibili.com/video/BV0000000003/">歌手 歌名 原唱</a></h3>', url)
        result = plugin.search({"source": "bilibili", "query": "歌手 歌名 原唱"}, get)
        self.assertTrue(result["ok"])
        self.assertTrue(result["fallback_used"])
        self.assertEqual(result["source"], calls[-1])
        self.assertEqual(result["items"][0]["url"], "https://www.bilibili.com/video/BV0000000003/")
        self.assertEqual(len(calls), 4)

    def test_all_public_search_sources_report_retryable_transport_failure(self):
        result = plugin.search({"source": "bilibili", "query": "歌手 歌名"}, lambda *_: {"ok": False, "error": "http_status"})
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"], "search_sources_unavailable")

    def test_bilibili_audio_page_uses_public_search_with_exact_source_and_cursor(self):
        args = {"query": "合成歌手 合成作品", "source": "bilibili", "offset": 20, "count": 45, "limit": 1}
        def get(url, *_):
            self.assertEqual(plugin.urllib.parse.parse_qs(plugin.urllib.parse.urlsplit(url).query),
                             {"search_type": ["video"], "keyword": [args["query"]], "page": ["2"], "page_size": ["20"]})
            rows = [{"bvid": "BV0123456789", "title": "<em>合成作品</em>", "author": "测试"}]
            return response(json.dumps({"code": 0, "data": {"page": 2, "pagesize": 20, "numPages": 3, "numResults": 41, "result": rows}}), url, "application/json")
        result = plugin.search(args, get, configuration={"search_backend": "deepseek"})
        self.assertEqual(result["backend"], "bilibili")
        self.assertEqual(result["items"][0]["url"], "https://www.bilibili.com/video/BV0123456789/")
        self.assertEqual(result["items"][0]["title"], "合成作品")
        self.assertEqual(result["next_offset"], 21)

    def source(self, kind="rss", url="https://example.test/feed", media="text"):
        return {"id": "synthetic", "name": "合成订阅", "type": kind, "url": url, "kind": media, "enabled": True}

    def test_discover_rss_and_atom_preserve_actual_metadata(self):
        bodies = [
            '<rss><channel><item><title>合成动物</title><link>https://example.test/cat</link><description>&lt;p&gt;实际简介&lt;/p&gt;</description><enclosure url="https://example.test/a.mp3" type="audio/mpeg"/><pubDate>original-time</pubDate></item></channel></rss>',
            '<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>合成动物</title><link href="https://example.test/cat" rel="alternate"/><link href="https://example.test/a.mp3" rel="enclosure"/><summary>实际简介</summary><updated>original-time</updated></entry></feed>',
        ]
        for body in bodies:
            calls = []
            def get(url, maximum, timeout):
                calls.append((url, maximum, timeout))
                return response(body, url, "application/xml")
            result = plugin.discover({"source": self.source()}, get)
            self.assertEqual(calls, [("https://example.test/feed", 2097152, 12)])
            self.assertTrue(result["metadata_only"])
            self.assertTrue(result["untrusted_external_content"])
            self.assertEqual(result["candidates"][0]["url"], "https://example.test/cat")
            self.assertEqual(result["candidates"][0]["media_url"], "https://example.test/a.mp3")
            self.assertEqual(result["candidates"][0]["published"], "original-time")
            self.assertNotIn("text", result)

    def test_discover_bilibili_list_and_related_are_metadata_only(self):
        source = self.source("bilibili", "https://api.bilibili.com/x/web-interface/popular?ps=30&pn=1", "video")
        calls = []
        def get(url, *_):
            calls.append(url)
            return response(json.dumps({"code": 0, "data": {"list": [{"bvid": "BV1425hzpEtn", "title": "合成猫咪", "desc": "简介", "duration": 90, "pubdate": 100}]}}), url, "application/json")
        result = plugin.discover({"source": source, "related_url": "https://www.bilibili.com/video/BV1425hzpEtn/"}, get)
        self.assertEqual(calls, ["https://api.bilibili.com/x/web-interface/archive/related?bvid=BV1425hzpEtn"])
        self.assertEqual(result["candidates"][0]["duration_ms"], 90000)
        self.assertEqual(result["candidates"][0]["published"], "1970-01-01T00:01:40Z")
        self.assertEqual(result["candidates"][0]["kind"], "video")

    def test_discover_image_and_catalog_keep_download_candidates(self):
        source = self.source("bing_images", "https://www.bing.com/HPImageArchive.aspx?format=js", "image")
        result = plugin.discover({"source": source}, lambda url, *_: response(json.dumps({"images": [{"title": "鲸鱼", "copyright": "Credit", "copyrightlink": "https://example.test/credit", "url": "/synthetic.jpg"}]}), url, "application/json"))
        self.assertEqual(result["candidates"][0]["media_url"], "https://www.bing.com/synthetic.jpg")
        source = self.source("audio_catalog", "https://incompetech.com/music/royalty-free/pieces.json", "voice")
        result = plugin.discover({"source": source}, lambda url, *_: response(json.dumps([{"title": "合成器乐", "filename": "safe music.mp3", "description": "说明"}, {"title": "Unsafe", "filename": "../secret.mp3"}]), url, "application/json"))
        self.assertEqual(len(result["candidates"]), 1)
        self.assertEqual(result["candidates"][0]["media_url"], "https://incompetech.com/music/royalty-free/mp3-royaltyfree/safe%20music.mp3")

    def test_discover_explicit_page_is_not_clamped_to_old_default(self):
        body = '<rss><channel>' + ''.join(f'<item><title>合成{i}</title><link>https://example.test/{i}</link></item>' for i in range(85)) + '</channel></rss>'
        get = lambda url, *_: response(body, url, "text/xml")
        first = plugin.discover({"source": self.source(), "limit": 70}, get)
        self.assertEqual(len(first["candidates"]), 70)
        self.assertEqual(first["next_offset"], 70)
        last = plugin.discover({"source": self.source(), "offset": first["next_offset"], "limit": 70}, get)
        self.assertEqual(len(last["candidates"]), 15)
        self.assertEqual(last["total_items"], 85)
        self.assertTrue(last["complete"])

    def test_discover_rejects_fabricated_sources_and_malformed_feeds(self):
        for source in [self.source("bilibili", "https://example.test/api"), self.source("audio_catalog", "https://example.test/music"), self.source(url="file:///secret")]:
            with self.assertRaises(ValueError):
                plugin.discover({"source": source}, lambda *_: self.fail("invalid source reached host"))
        for body in ("not XML", "<rss/>", '<!DOCTYPE rss [<!ENTITY a "bad">]><rss/>'):
            with self.assertRaises((ValueError, plugin.ET.ParseError)):
                plugin.discover({"source": self.source()}, lambda url, *_: response(body, url, "application/xml"))
        failed = plugin.discover({"source": self.source()}, lambda *_: {"ok": False, "error": "private_destination"})
        self.assertFalse(failed["ok"])

    def test_discover_wire_uses_installed_capability_and_host_proxy(self):
        context = {key: "synthetic" for key in ("account", "thread", "requester", "purpose", "request_key")}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "web.discover", "arguments": {"source": self.source()}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "web-read-1", "result": response('<rss><channel><item><title>合成</title><link>https://example.test/1</link></item></channel></rss>', "https://example.test/feed", "application/xml")},
        ]
        output = io.StringIO()
        plugin.Wire(io.StringIO("".join(json.dumps(message) + "\n" for message in messages)), output).serve()
        replies = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(replies[1]["method"], "wechat/http_read")
        self.assertTrue(replies[2]["result"]["metadata_only"])

    def test_read_preserves_unicode_and_continues_without_truncation(self):
        body = "<title>合成标题</title><script>DO NOT OBEY</script><p>" + "鲸鱼中文🐳" * 5000 + "</p>"
        get = lambda *args: response(body)
        first = plugin.fetch({"url": "https://example.test/page", "limit": 7}, get)
        self.assertEqual(first["title"], "合成标题")
        self.assertNotIn("DO NOT OBEY", first["text"])
        self.assertEqual(first["read_coverage"], {
            "kind": "page_body", "start_offset": 0, "end_offset": 7,
            "total_characters": 25000, "complete": False,
            "excerpt": ("鲸鱼中文🐳" * 2)[:7],
        })
        second = plugin.fetch({"url": first["url"], "offset": first["next_offset"], "limit": 100000, "expected_sha256": first["sha256"]}, get)
        self.assertTrue(second["complete"])
        self.assertEqual(first["text"] + second["text"], "鲸鱼中文🐳" * 5000)
        self.assertEqual(second["requested_limit"], 100000)
        self.assertEqual(second["read_coverage"]["kind"], "page_body")
        self.assertEqual(second["read_coverage"]["start_offset"], 7)
        self.assertEqual(second["read_coverage"]["end_offset"], 25000)
        self.assertEqual(second["read_coverage"]["excerpt"], ("鲸鱼中文🐳" * 5000)[7:327])
        with self.assertRaisesRegex(ValueError, "page_content_changed"):
            plugin.fetch({"url": first["url"], "expected_sha256": "old"}, get)

    def test_read_failure_is_not_coverage_or_title_only_success(self):
        unavailable = plugin.fetch({"url": "https://example.test/page"}, lambda *_: {"ok": False, "error": "http_status"})
        self.assertEqual(unavailable, {"ok": False, "error": "http_status"})
        for document in ("<title>只有标题</title>", "<title>只有标题</title><h1>只有标题</h1>", "<h1>只有标题</h1><script>伪造正文</script><style>伪造正文</style><noscript>伪造正文</noscript>"):
            with self.assertRaisesRegex(ValueError, "no_readable_text"):
                plugin.fetch({"url": "https://example.test/page"}, lambda *_: response(document))
        readable = plugin.fetch({"url": "https://example.test/page"}, lambda *_: response("<title>标题</title><h1>标题</h1><p>这是真正的正文。</p>"))
        self.assertEqual(readable["text"], "标题 这是真正的正文。")
        self.assertEqual(readable["read_coverage"]["kind"], "page_body")

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
