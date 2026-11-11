"""Focused synthetic replay of the independent surf application grant panel."""
import argparse
import json
from pathlib import Path
import re
import tempfile
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright, expect
from test_local_home_ui import start, stop, home_tab, open_configuration


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-surf-apps-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "work")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1280, "height": 740})
                errors, writes, reads = [], [], []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", lambda route: route.fulfill(json={"items": [{"id": "wechat", "name": "微信", "registration": "station-native"}]}))
                page.route("**/api/apps/wechat/connections", lambda route: route.fulfill(json={"items": [
                    {"id": "wechat-synthetic-one", "label": "合成账号一"}, {"id": "wechat-synthetic-two", "label": "合成账号二"}]}))
                states = {key: {"revision": 0, "scopes": [], "current": False} for key in ("one", "two")}
                available = [{"id": "synthetic-root", "target": {"app_id": "douyin", "installation_id": "synthetic", "connection_id": "web-page-text", "account_id": "synthetic-profile", "content_id": "home", "fields": ["page_text"]},
                              "fields": ["page_text", "comments", "visual"], "actions": ["like", "reply_comment"]}]
                pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-(one|two)/(surf(?:-reader|-share-grants|-app-grants)?|legacy-import/surf)$")
                def respond(route):
                    path = urlparse(route.request.url).path
                    account = "two" if "/wechat-synthetic-two/" in path else "one"
                    reads.append(path)
                    if path.endswith("/surf-app-grants"):
                        saved = states[account]
                        if route.request.method == "PUT":
                            assert route.request.headers.get("x-csrf-token")
                            body = route.request.post_data_json
                            assert body["revision"] == saved["revision"] and body["key"]
                            writes.append((account, body))
                            scopes = []
                            for chosen in body["scopes"]:
                                scope = dict(available[0], **chosen)
                                scopes.append(scope)
                            saved.update(revision=saved["revision"] + 1, scopes=scopes, current=bool(scopes))
                        route.fulfill(json=dict(saved, available=available))
                    elif path.endswith("/surf-share-grants"):
                        route.fulfill(json={"grant": {"revision": 0, "auto": False, "targets": {}, "sources": {}}, "current": False, "available_targets": [], "available_sources": {}})
                    elif path.endswith("/surf-reader"):
                        route.fulfill(json={"installed": True, "enabled": True, "revision": 1})
                    elif path.endswith("/legacy-import/surf"):
                        route.fulfill(json={"history_available": False})
                    elif path.endswith("/surf"):
                        route.fulfill(json={"revision": 1, "enabled": False,"public_reader_ready":False,"application_reader_ready":states[account]["current"], "settings": {"topics": ["合成主题"], "sources": [], "interval_minutes": 60, "interval_max_minutes": 150,
                            "daily_limit": 4, "tool_calls": 12, "timezone": "Asia/Shanghai", "quiet_start": "00:00", "quiet_end": "08:00"}})
                    else:
                        raise AssertionError(path)
                page.route(pattern, respond)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                home_tab(page, "apps")
                open_configuration(page, "微信", "网上冲浪")
                panel = page.locator("#app-configuration")
                app = panel.locator(".configuration-surf-apps")
                expect(app.get_by_text("应用冲浪未授权", exact=True)).to_be_visible()
                assert not writes and all("/observation" not in path for path in reads)
                app.locator("summary").click()
                app.get_by_label("启用此应用入口", exact=True).check()
                app.get_by_label("评论（后续入口）", exact=True).check()
                app.get_by_label("回复已读评论", exact=True).check()
                app.get_by_label("点赞与取消点赞", exact=True).check()
                page.keyboard.press("Escape")
                page.locator("#app-configuration-open").click()
                page.locator("#app-configuration-nav").get_by_role("button", name="网上冲浪", exact=True).click()
                expect(app.get_by_label("回复已读评论", exact=True)).to_be_checked()
                panel.get_by_label("当前连接").select_option("wechat-synthetic-two")
                expect(app.get_by_text("应用冲浪未授权", exact=True)).to_be_visible()
                app.locator("summary").click()
                expect(app.get_by_label("启用此应用入口", exact=True)).not_to_be_checked()
                panel.get_by_label("当前连接").select_option("wechat-synthetic-one")
                expect(app.get_by_label("回复已读评论", exact=True)).to_be_checked()
                app.get_by_role("button", name="保存应用冲浪授权", exact=True).click()
                expect(app.get_by_text("应用冲浪授权已保存。", exact=True)).to_be_visible()
                assert len(writes) == 1 and writes[0][0] == "one"
                selected = writes[0][1]["scopes"][0]
                assert selected["fields"] == ["comments", "page_text"] and selected["actions"] == ["like", "reply_comment"]
                confirm = panel.get_by_label("确认按应用冲浪授权启用调度", exact=True)
                expect(confirm).to_be_visible()
                confirm.check()
                expect(panel.get_by_role("button", name="启用冲浪", exact=True)).to_be_enabled()
                assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
                page.screenshot(path=str(output / "surf-apps-desktop-small.png"))
                app.get_by_role("button", name="清空应用冲浪授权", exact=True).click()
                expect(app.get_by_text("应用冲浪授权已清空。", exact=True)).to_be_visible()
                assert writes[-1][1]["scopes"] == [] and not errors, errors
                browser.close()
        finally:
            stop(process)
    print(json.dumps({"status": "passed", "scope": "new surf application grants only", "real_platform_actions": 0}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
