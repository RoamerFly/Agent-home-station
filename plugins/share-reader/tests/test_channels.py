import importlib.util
import json
from pathlib import Path
import sys
import unittest

sys.dont_write_bytecode = True
spec=importlib.util.spec_from_file_location("channels_adapter",Path(__file__).resolve().parents[1]/"runtime/channels.py")
adapter=importlib.util.module_from_spec(spec);spec.loader.exec_module(adapter)
SOURCE="https://weixin.qq.com/sph/synthetic123"


class ChannelsTests(unittest.TestCase):
    def fixture(self, mutate=None):
        calls=[]
        def route(name, body, timeout, referer=""):
            calls.append((name,body,timeout,referer))
            replies={
                "channels-shortlink":{"errCode":0,"data":{"shortUrl":SOURCE}},
                "channels-parse":{"code":0,"data":{"playable_url":"https://channels.weixin.qq.com/finder-preview/detail?token=synthetic-private&eid=export/synthetic"}},
                "channels-feed":{"errCode":0,"data":{"authorInfo":{"nickname":"合成作者"},"feedInfo":{"description":"合成视频标题","videoUrl":"https://media.example/video?token=synthetic-media","h264VideoInfo":{"videoUrl":"https://media.example/compatible?token=synthetic-media"},"decodeKey":"18446744073709551615","objectId":"12345678901234567890"}}}
            }
            value=replies[name]
            if mutate: mutate(name,value)
            return value
        return route,calls

    def test_shortlink_flow_uses_routes_not_credentials_and_not_observation(self):
        route,calls=self.fixture();out=adapter.resolve({"url":SOURCE+"?tracking=synthetic","expected_object_id":"12345678901234567890"},route)
        self.assertEqual([c[0] for c in calls],["channels-parse","channels-feed"])
        self.assertEqual(calls[0][1]["url"],SOURCE)
        self.assertEqual(out["source_url"],SOURCE)
        self.assertEqual(out["private_decode_key"],"18446744073709551615")
        self.assertEqual(out["private_media_locators"],["https://media.example/compatible?token=synthetic-media"])
        self.assertFalse(out["video_observed"]);self.assertFalse(out["media_downloaded"])
        self.assertNotIn("cookie",json.dumps(calls).lower())
        self.assertNotIn("synthetic-private",json.dumps(out))

    def test_export_object_has_separate_shortlink_route(self):
        route,calls=self.fixture();adapter.resolve({"export_id":"export/synthetic","nonce_id":"synthetic-nonce","timeout_seconds":90},route)
        self.assertEqual([c[0] for c in calls],["channels-shortlink","channels-parse","channels-feed"])
        self.assertTrue(all(c[2]==90 for c in calls))
        with self.assertRaisesRegex(ValueError,"ambiguous_channels_source"):
            adapter.resolve({"url":SOURCE,"export_id":"export/synthetic","nonce_id":"synthetic"},route)

    def test_wrong_work_and_uint64_never_accepted(self):
        route,_=self.fixture()
        with self.assertRaisesRegex(ValueError,"channels_resolved_video_mismatch"):
            adapter.resolve({"url":SOURCE,"expected_object_id":"9"},route)
        for bad in ["18446744073709551616",True,-1,1.5,"1e20"]:
            def mutate(name,value):
                if name=="channels-feed":value["data"]["feedInfo"]["decodeKey"]=bad
            route,_=self.fixture(mutate)
            with self.subTest(bad=bad),self.assertRaisesRegex(ValueError,"channels_decode_key_invalid"):
                adapter.resolve({"url":SOURCE},route)

    def test_untrusted_playable_endpoint_or_duplicate_token_not_followed(self):
        for play in ["https://evil.example/finder-preview/?token=x&eid=y","https://channels.weixin.qq.com.evil.example/finder-preview/?token=x&eid=y","https://user@channels.weixin.qq.com/finder-preview/?token=x&eid=y","https://channels.weixin.qq.com/finder-preview/?token=x&token=y&eid=z"]:
            def mutate(name,value):
                if name=="channels-parse":value["data"]["playable_url"]=play
            route,calls=self.fixture(mutate)
            with self.assertRaisesRegex(ValueError,"channels_playable_link_missing"):
                adapter.resolve({"url":SOURCE},route)
            self.assertEqual(len(calls),1)

    def test_share_identity_and_upstream_failure_are_not_summaries(self):
        for bad in ["http://weixin.qq.com/sph/synthetic123","https://weixin.qq.com.evil.example/sph/synthetic123","https://weixin.qq.com/sph/%73ynthetic123","https://user@weixin.qq.com/sph/synthetic123"]:
            with self.assertRaisesRegex(ValueError,"channels_share_link_invalid"):adapter.share_url(bad)
        def mutate(name,value):
            if name=="channels-parse":value["code"]=401
        route,calls=self.fixture(mutate)
        with self.assertRaisesRegex(ValueError,"channels_yuanbao_parse_failed"):adapter.resolve({"url":SOURCE},route)
        self.assertEqual(len(calls),1)


if __name__=="__main__":unittest.main()
