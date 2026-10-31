"""Bilibili protocol fixtures; no network, account cookies or model calls."""
import io
import json
import sys
import unittest
from urllib.parse import parse_qs, urlsplit

sys.dont_write_bytecode = True
from test_public_share import main, parser, response

BV = "BV0000000001"
PAGE = "https://www.bilibili.com/video/" + BV + "/"
MEDIA = "https://synthetic.bilivideo.com/video.mp4?synthetic_token=1"


def metadata(parts=2):
    return {"videoData": {"bvid": BV, "cid": 101, "title": "合成作品",
                          "pages": [{"cid": 101 + i, "part": "分集" + str(i + 1)} for i in range(parts)]}}


def playback():
    return {"code": 0, "data": {"timelength": 720000, "durl": [{"url": MEDIA, "size": 1234}]}}


def html(value):
    return "<script>window.__INITIAL_STATE__ = " + json.dumps(value) + ";neverExecute()</script>"


class BilibiliTests(unittest.TestCase):
    def provider(self, final=PAGE, state=None, play=None):
        calls = []
        def get(url, maximum, timeout):
            calls.append((url, maximum, timeout))
            if len(calls) == 1:
                return response(html(state if state is not None else metadata()), final)
            return response(json.dumps(play if play is not None else playback()), url, "application/json")
        return get, calls

    def test_selected_part_exact_cid_and_private_result(self):
        get, calls = self.provider(PAGE + "?p=2&tracking=synthetic")
        result = parser.resolve({"url": PAGE + "?p=2", "max_bytes": 3000000, "timeout_seconds": 47}, get)
        query = parse_qs(urlsplit(calls[1][0]).query)
        self.assertEqual(query, {"bvid": [BV], "cid": ["102"], "qn": ["32"], "fnval": ["0"], "fourk": ["0"]})
        self.assertEqual([c[1:] for c in calls], [(3000000, 47)] * 2)
        self.assertEqual(result["source_url"], PAGE + "?p=2")
        self.assertEqual(result["platform"], "bilibili")
        self.assertEqual(result["object_id"], BV)
        self.assertEqual(result["title"], "合成作品 · 分集2")
        self.assertEqual(result["duration_ms"], 720000)  # Metadata is not a download-duration ceiling.
        self.assertEqual(result["text"], "")
        self.assertEqual(result["private_media_locators"], [MEDIA])
        self.assertFalse(result["video_observed"])
        self.assertFalse(result["media_downloaded"])
        self.assertEqual(result["coverage"], "media_locator_only")

    def test_shortlink_uses_verified_redirect_but_never_foreign_page(self):
        get, calls = self.provider(PAGE + "?p=2")
        result = parser.resolve({"url": "https://b23.tv/Synthetic"}, get)
        self.assertEqual(result["source_url"], PAGE + "?p=2")
        self.assertEqual(len(calls), 2)
        for final in ["https://public.example/advert", "https://bilibili.com.evil.invalid/video/" + BV, "https://b23.tv/Synthetic"]:
            get, calls = self.provider(final)
            with self.subTest(final=final), self.assertRaises(ValueError):
                parser.resolve({"url": "https://b23.tv/Synthetic"}, get)
            self.assertEqual(len(calls), 1)

    def test_redirect_cannot_switch_work_or_selected_part(self):
        for final in [PAGE.replace(BV, "BV0000000002"), PAGE + "?p=2", "https://public.example/page"]:
            get, calls = self.provider(final)
            with self.subTest(final=final), self.assertRaisesRegex(ValueError, "share_identity_conflict"):
                parser.resolve({"url": PAGE}, get)
            self.assertEqual(len(calls), 1)

    def test_invalid_part_rejected_and_large_valid_part_supported(self):
        for query in ["p=0", "p=-1", "p=", "p=2&p=2", "p=1&p=2", "p=true", "p=1.5"]:
            get, calls = self.provider()
            with self.subTest(query=query), self.assertRaisesRegex(ValueError, "video_part_unavailable"):
                parser.resolve({"url": PAGE + "?" + query}, get)
            self.assertEqual(calls, [])
        get, _ = self.provider(PAGE + "?p=101", metadata(101))
        result = parser.resolve({"url": PAGE + "?p=101"}, get)
        self.assertEqual(result["source_url"], PAGE + "?p=101")
        get, calls = self.provider(PAGE + "?p=3")
        with self.assertRaisesRegex(ValueError, "video_part_unavailable"):
            parser.resolve({"url": PAGE + "?p=3"}, get)
        self.assertEqual(len(calls), 1)

    def test_metadata_identity_types_and_duplicates(self):
        for field, value in [("bvid", "BV0000000002"), ("cid", True), ("pages", {}), ("title", "")]:
            state = metadata(); state["videoData"][field] = value
            get, calls = self.provider(state=state)
            with self.subTest(field=field), self.assertRaises(ValueError):
                parser.resolve({"url": PAGE}, get)
            self.assertEqual(len(calls), 1)
        for state in ["window.__INITIAL_STATE__={\"videoData\":{},\"videoData\":{}}", "<article>" + "应用推荐" * 200 + "</article>"]:
            with self.assertRaises(ValueError):
                parser.resolve({"url": PAGE}, lambda *a: response(state, PAGE))
        state = metadata(); state["videoData"]["pages"][0]["cid"] = 999
        with self.assertRaisesRegex(ValueError, "share_identity_conflict"):
            parser.bili_metadata(html(state), (BV, 1))

    def test_restricted_and_non_progressive_streams_fail_without_substitution(self):
        variants = []
        for value in [True, 1, "1", "0", 0.0, None]:
            p = playback(); p["data"]["is_preview"] = value; variants.append(p)
        for field, value in [("timelength", True), ("timelength", 0), ("durl", []), ("durl", [{}, {}])]:
            p = playback(); p["data"][field] = value; variants.append(p)
        p = playback(); p["code"] = -10403; variants.append(p)
        for p in variants:
            get, calls = self.provider(play=p)
            with self.subTest(p=p), self.assertRaises(ValueError):
                parser.resolve({"url": PAGE}, get)
            self.assertEqual(len(calls), 2)
        for locator in ["https://synthetic.bilivideo.com.evil.invalid/a", "http://synthetic.bilivideo.com/a", "https://127.0.0.1/a", "https://user@synthetic.bilivideo.com/a", "https://synthetic.bilivideo.com:444/a"]:
            p = playback(); p["data"]["durl"][0]["url"] = locator
            with self.subTest(locator=locator), self.assertRaises(ValueError):
                parser.bili_playback(json.dumps(p))

    def test_api_redirect_response_limits_and_content_revision(self):
        get, calls = self.provider()
        def changed(url, maximum, timeout):
            r = get(url, maximum, timeout)
            if len(calls) == 2:
                r["url"] = url.replace("cid=101", "cid=999")
            return r
        with self.assertRaisesRegex(ValueError, "share_identity_conflict"):
            parser.resolve({"url": PAGE}, changed)
        get, _ = self.provider()
        with self.assertRaisesRegex(ValueError, "host_response_exceeds_requested_bytes"):
            parser.resolve({"url": PAGE, "max_bytes": 20}, get)
        get, _ = self.provider(); first = parser.resolve({"url": PAGE}, get)
        p = playback(); p["data"]["durl"][0]["url"] += "changed"
        get, _ = self.provider(play=p)
        with self.assertRaisesRegex(ValueError, "share_content_changed"):
            parser.resolve({"url": PAGE, "expected_sha256": first["content_sha256"]}, get)

    def test_stdio_executes_real_parser_with_two_bound_host_reads(self):
        context = {k: "synthetic" for k in ["account", "thread", "requester", "purpose", "request_key"]}
        api = "https://api.bilibili.com/x/player/playurl?bvid=" + BV + "&cid=101&qn=32&fnval=0&fourk=0"
        frames = [{"jsonrpc": "2.0", "id": 1, "method": "initialize"},
                  {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "share.resolve_public", "arguments": {"url": PAGE}, "_meta": {"wechat/context": context}}},
                  {"jsonrpc": "2.0", "id": "share-read-1", "result": response(html(metadata()), PAGE)},
                  {"jsonrpc": "2.0", "id": "share-read-2", "result": response(json.dumps(playback()), api, "application/json")}]
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(f) for f in frames) + "\n"), output).serve()
        values = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual([v.get("method") for v in values[1:3]], ["wechat/http_read"] * 2)
        self.assertEqual(values[2]["params"]["url"], api)
        self.assertTrue(values[-1]["result"]["ok"])
        self.assertEqual(values[-1]["result"]["object_id"], BV)


if __name__ == "__main__":
    unittest.main()
