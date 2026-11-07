"""Offline acceptance for ordinary Douyin web-login UI; it never opens Douyin."""

import argparse
import json
from pathlib import Path
import re
import tempfile

from playwright.sync_api import sync_playwright

from test_local_home_ui import assert_no_horizontal_overflow, fulfill_json, home_tab, open_application, start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-douyin-web-login-", dir=output) as directory:
        root = Path(directory).resolve()
        process, origin = start(binary, root / "host", root / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                for width, color, suffix in ((375, "dark", "mobile-dark"), (1280, "light", "desktop-light")):
                    context = browser.new_context(viewport={"width": width, "height": 800}, color_scheme=color)
                    page = context.new_page()
                    dialogs = []
                    page.on("dialog", lambda dialog: dialogs.append(dialog.type))
                    page.route(re.compile(r".*/api/local/apps/catalog$"), fulfill_json({"applications": [{"id": "douyin", "name": "抖音", "install_state": "installed", "source_available": True}]}))
                    page.route(re.compile(r".*/api/apps$"), fulfill_json({"items": [{"id": "douyin", "name": "抖音"}]}))
                    page.route("**/api/apps/douyin/connections", fulfill_json({"items": []}))
                    page.route("**/api/local/apps/douyin/authorization", fulfill_json({"revision": "", "configured": False, "status": "unconfigured", "label": "", "works_authorized": False, "publish_authorized": False}))
                    page.route(re.compile(r".*/api/local/publications\?app_id=douyin(?:&.*)?$"), fulfill_json({"tasks": {"items": [], "next": 0, "has_more": False}, "targets": []}))

                    revision = 1
                    begin_count = 0
                    view = {"revision": revision, "state": "unconfigured", "active": False, "available": True}
                    requests = []

                    def web_login(route):
                        nonlocal revision, view, begin_count
                        if route.request.method == "GET":
                            fulfill_json(view)(route)
                            return
                        body = route.request.post_data_json
                        requests.append(body)
                        action = body.get("action")
                        assert body.get("revision") == revision
                        if action == "begin":
                            assert body == {"action": "begin", "revision": revision, "approve_profile": True}
                            begin_count += 1
                            revision += 1
                            view = {"revision": revision, "state": "login_open", "active": True, "available": True, "attempt_id": f"attempt-{begin_count}"}
                        elif action == "confirm":
                            assert body == {"action": "confirm", "revision": revision, "attempt_id": view["attempt_id"]}
                            revision += 1
                            view = {"revision": revision, "state": "saved", "active": False, "available": True, "confirmed_at": "2030-01-02T03:04:05Z"}
                        elif action == "clear":
                            assert body == {"action": "clear", "revision": revision, "approve_profile": True}
                            revision += 1
                            view = {"revision": revision, "state": "unconfigured", "active": False, "available": True}
                        elif action == "cancel":
                            assert body == {"action": "cancel", "revision": revision, "attempt_id": view["attempt_id"]}
                            revision += 1
                            view = {"revision": revision, "state": "unconfigured", "active": False, "available": True}
                        else:
                            raise AssertionError(f"unexpected web-login action: {action}")
                        fulfill_json(view)(route)

                    page.route("**/api/local/apps/douyin/web-login", web_login)
                    page.goto(origin)
                    page.locator("#login-password").fill("synthetic-home-owner")
                    page.locator("#login-submit").click()
                    page.locator("#home-view").wait_for(state="visible")
                    if page.locator("#initialize-button").is_visible(timeout=500):
                        page.locator("#initialize-button").click()
                        page.locator("#initialize-button").wait_for(state="hidden")
                    home_tab(page, "apps")
                    page.locator("#apps-list[aria-busy='false']").wait_for()
                    open_application(page, "抖音")
                    page.get_by_text("尚未保存网页登录资料", exact=True).wait_for()
                    assert page.get_by_text("账号已验证", exact=True).count() == 0
                    assert page.get_by_text("已取得开放平台接口权限", exact=False).count() == 0
                    page.get_by_role("button", name="打开网页登录窗口", exact=True).click()
                    page.get_by_text("网页登录窗口已打开", exact=True).wait_for()
                    page.get_by_role("button", name="我已完成登录，保存", exact=True).wait_for()
                    # The next ordinary polling response may only move to an unconfirmed state.
                    view = {"revision": revision, "state": "unconfirmed", "active": False, "available": True, "attempt_id": "attempt-1"}
                    page.locator("#app-dialog-close").click()
                    open_application(page, "抖音")
                    page.get_by_text("等待主人确认", exact=True).wait_for()
                    page.get_by_role("button", name="我已完成登录，保存", exact=True).click()
                    page.get_by_text("网页登录资料已保存", exact=True).wait_for()
                    page.get_by_text("最近主人确认：", exact=False).wait_for()
                    assert page.get_by_text("登录有效性以网页为准，不代表已取得开放平台接口权限。", exact=True).count() == 1
                    # A saved profile can be opened again for a later re-login.
                    page.get_by_role("button", name="重新打开登录窗口", exact=True).click()
                    page.get_by_text("网页登录窗口已打开", exact=True).wait_for()
                    view = {"revision": revision, "state": "unconfirmed", "active": False, "available": True, "attempt_id": "attempt-2"}
                    page.locator("#app-dialog-close").click()
                    open_application(page, "抖音")
                    page.get_by_text("等待主人确认", exact=True).wait_for()
                    # Removing old profile data stays available even after the browser window is gone.
                    page.get_by_role("button", name="移除网页登录资料", exact=True).click()
                    page.get_by_text("尚未保存网页登录资料", exact=True).wait_for()

                    # A new attempt gets a new opaque identifier; cancelling uses that exact identifier.
                    page.get_by_role("button", name="打开网页登录窗口", exact=True).click()
                    page.get_by_role("button", name="取消本次登录", exact=True).click()
                    page.get_by_text("尚未保存网页登录资料", exact=True).wait_for()
                    assert len(requests) == 6
                    assert not dialogs, "网页登录操作不得弹出浏览器原生确认框"
                    assert_no_horizontal_overflow(page, f"{width}-pixel Douyin web login")
                    page.locator("#app-dialog").screenshot(path=str(output / f"douyin-web-login-{suffix}.png"))

                    # Closing the dialog and logging out must not let a pending UI poll repopulate the old detail.
                    page.locator("#app-dialog-close").click()
                    page.locator("#local-logout-button").click()
                    page.locator("#login-view").wait_for(state="visible")
                    assert not page.locator("#app-dialog").evaluate("node => node.open")
                    context.close()
                browser.close()
        finally:
            stop(process)
    print("Douyin ordinary web-login UI replay passed (synthetic local API only, no platform requests).")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
