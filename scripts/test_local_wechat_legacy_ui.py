"""Replay the imported Nov 4 canvas with synthetic account-scoped APIs."""
import argparse
import copy
import hashlib
import json
import tempfile
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright
from test_local_home_ui import app_connection_payload, fulfill_json, open_configuration, start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="legacy-canvas-", dir=output) as temporary:
        root = Path(temporary).resolve()
        process, origin = start(binary, root / "data", root / "workspace")
        reads, saves, errors = [], [], []
        default = json.loads((Path(__file__).resolve().parents[1] / "internal/wechatui/static/flow-default.json").read_text(encoding="utf-8"))
        accounts = {}

        def canvas(route):
            connection = urlparse(route.request.url).path.split("/")[-2]
            document = accounts.setdefault(connection, copy.deepcopy(default))
            revision = hashlib.sha256(json.dumps(document).encode()).hexdigest()
            if route.request.method == "POST":
                payload = route.request.post_data_json
                assert route.request.headers.get("x-csrf-token"), "legacy save lost owner CSRF"
                if payload["base_revision"] != revision:
                    return fulfill_json({"error": "画布已在其他页面更新"}, 409)(route)
                document = accounts[connection] = payload["document"]
                revision = hashlib.sha256(json.dumps(document).encode()).hexdigest()
                saves.append(connection)
            else:
                reads.append(connection)
            fulfill_json({"document": document, "revision": revision, "saved": connection in saves,
                          "connection": {"id": connection, "login": "signed_out", "worker": "disabled", "configured_policy": {}}})(route)

        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1440, "height": 1000}, reduced_motion="reduce")
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", fulfill_json(app_connection_payload()))
                page.route("**/api/apps/wechat/connections", fulfill_json({"items": [
                    {"id": "synthetic-one", "label": "合成账号一", "primary": True},
                    {"id": "synthetic-two", "label": "合成账号二"},
                ]}))
                page.route("**/api/local/apps/wechat/configuration", fulfill_json({
                    "id": "wechat", "name": "微信", "registered": True,
                    "sections": [{"id": "overview", "title": "概览"}, {"id": "flow", "title": "流程与状态"},
                                 {"id": "tasks", "title": "任务与文档"}, {"id": "account", "title": "账号与授权"}],
                }))
                page.route("**/connections/*/canvas", canvas)
                page.route("**/execution-rounds?*", fulfill_json({"steps": [], "next_before": ""}))
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                page.locator("#apps-tab").click()
                page.locator("#apps-list[aria-busy='false']").wait_for()
                open_configuration(page, "微信", "概览")
                assert not reads, "opening the overview eagerly read the old canvas"
                open_configuration(page, "微信", "流程与状态")
                frame = page.frame_locator(".wechat-workbench-frame")
                expect(frame.locator(".flow-node")).to_have_count(24)
                page.screenshot(path=str(output / "legacy-flow-graph.png"), full_page=True, animations="disabled")
                frame.locator("#flow-name").fill("合成账号一的流程")
                frame.locator("#flow-name").press("Tab")
                frame.locator("#flow-save").click()
                expect(frame.locator("#flow-save-state")).to_have_text("已保存")
                assert saves == ["synthetic-one"]

                # The real legacy editor still owns node editing, undo and drafts.
                frame.locator("#flow-view-list").click()
                frame.locator("#flow-node-receive .flow-node-card").click()
                inspector = frame.locator("#flow-inspector")
                inspector.get_by_label("节点名称", exact=True).fill("合成接收节点")
                inspector.get_by_role("button", name="关闭", exact=True).click()
                frame.locator("#flow-undo").click()
                expect(frame.locator("#flow-node-receive .flow-node-title")).to_have_text(default["nodes"][0]["title"])
                frame.locator("#flow-redo").click()
                expect(frame.locator("#flow-node-receive .flow-node-title")).to_have_text("合成接收节点")
                page.wait_for_timeout(200)  # legacy draft debounce is 160ms
                assert page.evaluate("Object.keys(sessionStorage).some(key=>key.endsWith(':synthetic-one'))")
                page.screenshot(path=str(output / "legacy-canvas.png"), full_page=True, animations="disabled")

                selector = page.locator("#app-configuration-connection")
                selector.select_option("synthetic-two")
                frame = page.frame_locator(".wechat-workbench-frame")
                expect(frame.locator(".flow-node")).to_have_count(24)
                expect(frame.locator("#flow-name")).to_have_value(default["name"])
                assert not frame.locator(".flow-node-title").filter(has_text="合成接收节点").count()
                selector.select_option("synthetic-one")
                frame = page.frame_locator(".wechat-workbench-frame")
                expect(frame.locator("#flow-node-receive .flow-node-title")).to_have_text("合成接收节点")
                expect(frame.locator("#flow-save-state")).to_have_text("有未保存更改")
                page.set_viewport_size({"width": 1280, "height": 900})
                assert frame.locator("body").evaluate("node=>node.scrollWidth<=innerWidth"), "desktop canvas overflow"

                # A current account frame can open the shared station tools.
                frame.locator("#flow-view-list").click()
                frame.locator("#flow-node-model .flow-node-card").click()
                frame.locator("#flow-inspector-tabs").get_by_role("button", name="配置", exact=True).click()
                frame.get_by_role("button", name="打开模型与语音", exact=True).click()
                expect(page.locator("#settings-tab")).to_have_attribute("aria-selected", "true")
                assert not page.locator("#app-configuration").evaluate("node=>node.open")
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
        (output / "result.json").write_text(json.dumps({
            "legacy_editor": True, "lazy_canvas": True, "account_drafts_isolated": True,
            "node_edit_undo_redo": True, "shared_model_navigation": True,
            "reads": reads, "saves": saves, "browser_errors": errors,
        }, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
