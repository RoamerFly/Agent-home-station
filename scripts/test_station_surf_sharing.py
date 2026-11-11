"""Focused replay for new surfing share controls; synthetic HTTP, no real sends."""
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
    with tempfile.TemporaryDirectory(prefix="synthetic-surf-sharing-", dir=output) as temporary:
        root = Path(temporary)
        process, origin = start(binary, root / "host", root / "work")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                context = browser.new_context(viewport={"width": 375, "height": 740})
                page = context.new_page()
                errors, reads, writes, shares = [], [], [], []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", lambda route: route.fulfill(json={"items": [{"id": "wechat", "name": "微信", "registration": "station-native"}]}))
                page.route("**/api/apps/wechat/connections", lambda route: route.fulfill(json={"items": [
                    {"id": "wechat-synthetic-one", "label": "合成账号一"}, {"id": "wechat-synthetic-two", "label": "合成账号二"}]}))
                pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-(one|two)/(surf(?:-reader|-share-grants|-shares|-history)?|legacy-import/surf)(?:[/?]|$)")
                grants = {account: {"revision": 0, "auto": False, "sources": {}, "targets": {}} for account in ("one", "two")}
                def route_app(route):
                    request = route.request
                    path = urlparse(request.url).path
                    account = "two" if "/wechat-synthetic-two/" in path else "one"
                    grant = grants[account]
                    if request.method == "GET":
                        reads.append(path)
                    if path.endswith("/surf-share-grants"):
                        if request.method == "PUT":
                            assert request.headers.get("x-csrf-token")
                            body = request.post_data_json
                            assert body["revision"] == grant["revision"] and body["key"]
                            writes.append((account, body))
                            grant.update(revision=grant["revision"] + 1, auto=body["auto"], sources={item: "synthetic-source-fingerprint" for item in body["sources"]},
                                         targets={item: "2026-11-11T00:00:00Z" for item in body["targets"]})
                        route.fulfill(json={"grant": grant, "current": bool(grant["targets"]), "available_targets": ["target@chatroom"], "available_sources": {"synthetic": "synthetic-source-fingerprint"}})
                    elif path.endswith("/surf-shares"):
                        assert request.method == "POST" and request.headers.get("x-csrf-token")
                        body = request.post_data_json
                        assert body["job"] == "synthetic-job" and body["target"] == "target@chatroom"
                        shares.append(body)
                        route.fulfill(json={"state": "unknown", "idempotency_key": "surf-share-synthetic-task"})
                    elif path.endswith("/surf-reader"):
                        route.fulfill(json={"installed": True, "enabled": True, "revision": 1})
                    elif path.endswith("/legacy-import/surf"):
                        route.fulfill(json={"settings_status": "missing", "history_available": False})
                    elif path.endswith("/surf-history"):
                        route.fulfill(json={"source_version": "synthetic-version", "rows": [{"job": "synthetic-job", "title": "合成可分享网页", "at": "2026-11-11T00:00:00Z", "state": "completed",
                            "source_id": "synthetic", "readable": True, "verified_public_read": True, "returned_characters": 8, "total_characters": 8, "offset": 0}]})
                    elif path.endswith("/surf"):
                        route.fulfill(json={"revision": 1, "enabled": False, "settings": {"topics": ["合成主题"], "sources": [], "interval_minutes": 60, "interval_max_minutes": 150,
                            "daily_limit": 4, "tool_calls": 12, "timezone": "Asia/Shanghai", "quiet_start": "00:00", "quiet_end": "08:00"}})
                    else:
                        raise AssertionError("unexpected new share route: " + path)
                page.route(pattern, route_app)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                home_tab(page, "apps")
                open_configuration(page, "微信", "网上冲浪")
                panel = page.locator("#app-configuration")
                sharing = panel.locator(".configuration-surf-sharing")
                expect(sharing.get_by_text("分享未授权", exact=True)).to_be_visible()
                assert not writes and not shares and not any(path.endswith("/surf-history") for path in reads)
                sharing.get_by_label("target@chatroom", exact=True).check()
                sharing.get_by_label("synthetic", exact=True).check()
                sharing.get_by_label("允许自主分享已读内容", exact=True).check()
                # Closing and switching account preserve only the original account's draft.
                page.keyboard.press("Escape")
                page.locator("#app-configuration-open").click()
                page.locator("#app-configuration-nav").get_by_role("button", name="网上冲浪", exact=True).click()
                expect(sharing.get_by_label("允许自主分享已读内容", exact=True)).to_be_checked()
                panel.get_by_label("当前连接").select_option("wechat-synthetic-two")
                expect(sharing.get_by_label("target@chatroom", exact=True)).not_to_be_checked()
                panel.get_by_label("当前连接").select_option("wechat-synthetic-one")
                expect(sharing.get_by_label("target@chatroom", exact=True)).to_be_checked()
                sharing.get_by_role("button", name="保存分享授权", exact=True).click()
                expect(sharing.get_by_text("分享授权已保存。", exact=True)).to_be_visible()
                assert len(writes) == 1 and writes[0][0] == "one" and writes[0][1]["auto"] is True
                history = panel.locator(".configuration-surf-live-history")
                history.get_by_role("button", name="读取本机浏览记录", exact=True).click()
                history.get_by_text("合成可分享网页", exact=False).click()
                history.get_by_role("button", name="分享此来源", exact=True).click()
                expect(history.get_by_label("分享到", exact=True)).to_be_visible()
                assert not shares and not any(path.endswith("/synthetic-job") for path in reads)
                history.get_by_label("分享说明（可选）", exact=True).fill("合成分享草稿")
                history.get_by_role("button", name="确认分享链接", exact=True).click()
                expect(history.get_by_text("结果未知，请查原任务", exact=False)).to_be_visible()
                assert len(shares) == 1 and shares[0]["description"] == "合成分享草稿"
                assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
                page.screenshot(path=str(output / "surf-share-narrow.png"))
                sharing.get_by_role("button", name="清空分享授权", exact=True).click()
                expect(sharing.get_by_text("分享授权已清空。", exact=True)).to_be_visible()
                assert writes[-1][1]["targets"] == [] and writes[-1][1]["sources"] == [] and not writes[-1][1]["auto"]
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
    print(json.dumps({"status": "passed", "scope": "new surf sharing controls only", "real_sends": 0}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
