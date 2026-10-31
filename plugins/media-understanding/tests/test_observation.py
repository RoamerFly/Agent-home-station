"""All model responses are synthetic; no HTTP or paid model invocation."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("media_service", Path(__file__).resolve().parents[1]/"runtime/main.py")
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)
observe = main.observation.observe
IMAGE = {"media_ref":"frame_1","sha256":"a"*64,"version":2,"mime":"image/png"}
CONFIG = {"vision_model":"synthetic-model","vision_backend":"deepseek","thinking":True,"reasoning_effort":"high"}
REPLY = {"choices":[{"message":{"content":"合成观察","reasoning_content":"不得输出的隐藏推理"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"total_tokens":112,"prompt_tokens_details":{"cached_tokens":80}}}


class ObservationTests(unittest.TestCase):
    def test_actual_request_uses_references_config_and_explicit_budget(self):
        captured=[]
        def call(request): captured.append(request); return copy.deepcopy(REPLY)
        args={"images":[IMAGE],"max_tokens":2000,"timeout_seconds":125,"thinking":False}
        out=observe(args,CONFIG,call)
        body=captured[0]["body"]
        self.assertEqual(body["model"],"synthetic-model")
        self.assertEqual(body["max_tokens"],2000)
        self.assertEqual(body["thinking"],{"type":"disabled"})
        self.assertNotIn("reasoning_effort",body)
        self.assertEqual(body["messages"][1]["content"][1]["image_url"]["url"],{"$media_ref":IMAGE})
        self.assertEqual(out["usage"]["cached_input_tokens"],80)
        self.assertNotIn("隐藏推理",json.dumps(out,ensure_ascii=False))
        self.assertFalse(out["audio_observed"])

    def test_thinking_and_arbitrary_image_count_are_not_silently_clamped(self):
        args={"images":[dict(IMAGE,media_ref="frame_"+str(i)) for i in range(12)]}
        def call(request):
            self.assertEqual(len(request["body"]["messages"][1]["content"]),13)
            self.assertEqual(request["body"]["reasoning_effort"],"high")
            return REPLY
        self.assertEqual(observe(args,CONFIG,call)["status"],"observed")

    def test_partial_output_and_unknown_usage_are_explicit(self):
        reply=copy.deepcopy(REPLY);reply["choices"][0]["finish_reason"]="length";del reply["usage"]
        out=observe({"images":[IMAGE]},CONFIG,lambda _:reply)
        self.assertEqual(out["status"],"partial")
        self.assertEqual(out["usage"],{"source":"unknown"})
        reply["usage"]={"prompt_tokens":1,"completion_tokens":2,"total_tokens":10}
        self.assertEqual(observe({"images":[IMAGE]},CONFIG,lambda _:reply)["usage"],{"source":"unknown"})

    def test_missing_or_forged_reference_rejected_without_call(self):
        invalid=[[],[IMAGE,IMAGE],[dict(IMAGE,media_ref="../../private")],[dict(IMAGE,version=True)],[dict(IMAGE,sha256="bad")],[dict(IMAGE,mime="video/mp4")],[dict(IMAGE,path="C:/private")]]
        for images in invalid:
            with self.subTest(images=images),self.assertRaises(ValueError):
                observe({"images":images},CONFIG,lambda _:self.fail("model called"))
        for extra in ({"endpoint":"https://other.example/"},{"model":"expensive-model"},{"max_tokens":True},{"thinking":"false"}):
            with self.subTest(extra=extra),self.assertRaises(ValueError):
                observe({"images":[IMAGE],**extra},CONFIG,lambda _:self.fail("model called"))

    def test_no_tool_calls_empty_response_or_moderation_as_observation(self):
        for change in ({"finish_reason":"content_filter"},{"message":{"content":"","reasoning_content":"reasoning only"}},{"message":{"content":"text","tool_calls":[{}]}}):
            reply=copy.deepcopy(REPLY);reply["choices"][0].update(change)
            with self.subTest(change=change),self.assertRaises(ValueError):observe({"images":[IMAGE]},CONFIG,lambda _:reply)
        with self.assertRaisesRegex(ValueError,"vision_provider_failed"):
            observe({"images":[IMAGE]},CONFIG,lambda _:{"error":{"message":"private provider failure"}})

    def test_unknown_thinking_protocol_is_not_silently_ignored(self):
        with self.assertRaisesRegex(ValueError,"thinking_protocol_not_configured"):
            observe({"images":[IMAGE]},dict(CONFIG,vision_backend="openai-compatible"),lambda _:self.fail("model called"))

    def test_stdio_roundtrip_scoped_context_and_no_private_initialization_fields(self):
        ctx={k:"synthetic" for k in ("account","thread","requester","purpose","request_key")}
        messages=[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"wechat/configuration":{"values":CONFIG}}}},
                  {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"media.observe","arguments":{"images":[IMAGE]},"_meta":{"wechat/context":ctx}}},
                  {"jsonrpc":"2.0","id":"media-model-1","result":REPLY}]
        output=io.StringIO();main.Wire(io.StringIO("\n".join(json.dumps(x) for x in messages)+"\n"),output).serve()
        rows=[json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows[1]["method"],"wechat/model_request")
        self.assertEqual(rows[2]["result"]["observation"],"合成观察")
        self.assertEqual(rows[2]["result"]["sources"],[IMAGE])


if __name__ == "__main__": unittest.main()
