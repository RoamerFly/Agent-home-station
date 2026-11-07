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
                    page.add_init_script("""(() => {
                        window.fixtureDelayWebLogin = false;
                        const nativeFetch = window.fetch.bind(window);
                        window.fetch = (...args) => {
                            const target = typeof args[0] === 'string' ? args[0] : args[0]?.url || '';
                            const response = nativeFetch(...args);
                            return String(target).includes('/api/local/apps/douyin/web-login') && window.fixtureDelayWebLogin
                              ? response.then(value => new Promise(resolve => setTimeout(() => resolve(value), 1000)))
                              : response;
                        };
                    })();""")
                    dialogs = []
                    page.on("dialog", lambda dialog: dialogs.append(dialog.type))
                    page.route(re.compile(r".*/api/local/apps/catalog$"), fulfill_json({"applications": [{"id": "douyin", "name": "抖音", "install_state": "installed", "source_available": True}]}))
                    page.route(re.compile(r".*/api/apps$"), fulfill_json({"items": [{"id": "douyin", "name": "抖音"}]}))
                    page.route("**/api/apps/douyin/connections", fulfill_json({"items": []}))
                    page.route("**/api/local/apps/douyin/authorization", fulfill_json({"revision": "", "configured": False, "status": "unconfigured", "label": "", "works_authorized": False, "publish_authorized": False}))
                    page.route(re.compile(r".*/api/local/publications\?app_id=douyin(?:&.*)?$"), fulfill_json({"tasks": {"items": [], "next": 0, "has_more": False}, "targets": []}))

                    revision = 1
                    begin_count = 0
                    late_401 = False
                    scope_conflict = False
                    playback_conflict = False
                    get_count = 0
                    # Older station responses did not include read_enabled; the UI must treat that as false.
                    view = {"revision": revision, "state": "unconfigured", "active": False, "available": True}
                    requests = []

                    def web_login(route):
                        nonlocal revision, view, begin_count, late_401, scope_conflict, playback_conflict, get_count
                        if route.request.method == "GET":
                            get_count += 1
                            if late_401:
                                late_401 = False
                                route.fulfill(status=401, content_type="application/json", body=json.dumps({"error": "登录状态已失效，请重新验证。"}, ensure_ascii=False))
                                return
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
                            view = {"revision": revision, "state": "login_open", "active": True, "available": True, "attempt_id": f"attempt-{begin_count}", "read_enabled": False, "read_scope": "home", "playback_enabled": False}
                        elif action == "confirm":
                            assert body == {"action": "confirm", "revision": revision, "attempt_id": view["attempt_id"]}
                            revision += 1
                            view = {"revision": revision, "state": "saved", "active": False, "available": True, "confirmed_at": "2030-01-02T03:04:05Z", "read_enabled": False, "read_scope": "home"}
                        elif action == "set_read":
                            assert set(body) <= {"action", "revision", "allow_read", "read_scope", "allow_playback"}
                            assert isinstance(body["allow_read"], bool)
                            assert "allow_playback" not in body or isinstance(body["allow_playback"], bool)
                            assert "read_scope" not in body or body["read_scope"] in ("home", "home_and_videos")
                            assert view["state"] == "saved" and not view["active"] and "attempt_id" not in view
                            if playback_conflict:
                                playback_conflict = False
                                assert body == {"action": "set_read", "revision": revision, "allow_read": True, "read_scope": "home_and_videos", "allow_playback": True}
                                revision += 1
                                view = {**view, "revision": revision, "playback_enabled": False}
                                route.fulfill(status=409, content_type="application/json", body=json.dumps({"error": "播放许可已在别处更新，请重新读取。"}, ensure_ascii=False))
                                return
                            if scope_conflict:
                                scope_conflict = False
                                assert body == {"action": "set_read", "revision": revision, "allow_read": True, "read_scope": "home"}
                                route.fulfill(status=409, content_type="application/json", body=json.dumps({"error": "读取范围已在别处更新，请重新读取。"}, ensure_ascii=False))
                                return
                            revision += 1
                            read_scope = body.get("read_scope", view.get("read_scope", "home")) if body["allow_read"] else "home"
                            playback_enabled = body.get("allow_playback", view.get("playback_enabled", False)) if body["allow_read"] and read_scope == "home_and_videos" else False
                            view = {**view, "revision": revision, "read_enabled": body["allow_read"], "read_scope": read_scope, "playback_enabled": playback_enabled}
                        elif action == "clear":
                            assert body == {"action": "clear", "revision": revision, "approve_profile": True}
                            revision += 1
                            view = {"revision": revision, "state": "unconfigured", "active": False, "available": True, "read_enabled": False, "read_scope": "home", "playback_enabled": False}
                        elif action == "cancel":
                            assert body == {"action": "cancel", "revision": revision, "attempt_id": view["attempt_id"]}
                            revision += 1
                            view = {"revision": revision, "state": "unconfigured", "active": False, "available": True, "read_enabled": False, "read_scope": "home", "playback_enabled": False}
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
                    assert page.get_by_role("button", name="允许读取首页文字", exact=True).count() == 0
                    assert page.get_by_label("读取范围", exact=True).count() == 0
                    assert page.get_by_label("允许静音观察视频播放", exact=True).count() == 0
                    assert page.get_by_text("账号已验证", exact=True).count() == 0
                    assert page.get_by_text("已取得开放平台接口权限", exact=False).count() == 0
                    page.get_by_role("button", name="打开网页登录窗口", exact=True).click()
                    page.get_by_text("网页登录窗口已打开", exact=True).wait_for()
                    page.get_by_role("button", name="我已完成登录，保存", exact=True).wait_for()
                    # The next ordinary polling response may only move to an unconfirmed state.
                    view = {"revision": revision, "state": "unconfirmed", "active": False, "available": True, "attempt_id": "attempt-1", "read_enabled": False, "read_scope": "home"}
                    page.locator("#app-dialog-close").click()
                    open_application(page, "抖音")
                    page.get_by_text("等待主人确认", exact=True).wait_for()
                    assert page.get_by_role("button", name="允许读取首页文字", exact=True).count() == 0
                    assert page.get_by_label("读取范围", exact=True).count() == 0
                    page.get_by_role("button", name="我已完成登录，保存", exact=True).click()
                    page.get_by_text("网页登录资料已保存", exact=True).wait_for()
                    page.get_by_text("最近主人确认：", exact=False).wait_for()
                    assert page.get_by_text("登录有效性以网页为准，不代表已取得开放平台接口权限。", exact=True).count() == 1
                    page.get_by_role("button", name="允许读取首页文字", exact=True).click()
                    page.get_by_role("button", name="停止网页读取", exact=True).wait_for()
                    page.get_by_text("已允许读取实际网页文字。", exact=False).wait_for()
                    scope = page.get_by_label("读取范围", exact=True)
                    assert scope.input_value() == "home"
                    assert page.get_by_label("允许静音观察视频播放", exact=True).count() == 0
                    scope.select_option("home_and_videos")
                    page.get_by_text("视频页文字不等于音轨或完整观看", exact=False).wait_for()
                    assert scope.input_value() == "home_and_videos"
                    # An older wide-text response has no playback flag; reopening must not grant it.
                    view.pop("playback_enabled", None)
                    page.locator("#app-dialog-close").click()
                    open_application(page, "抖音")
                    playback = page.get_by_label("允许静音观察视频播放", exact=True)
                    playback.wait_for()
                    assert not playback.is_checked(), "扩大文字范围不得自动开启播放许可；旧状态缺省关闭"
                    assert "allow_playback" not in requests[-1]
                    playback.check()
                    page.get_by_text("已允许静音观察视频播放。", exact=True).wait_for()
                    assert view["playback_enabled"] is True
                    playback.uncheck()
                    page.get_by_text("已停止静音播放观察。", exact=True).wait_for()
                    assert view["playback_enabled"] is False
                    before_gets = get_count
                    playback_conflict = True
                    # A failed save intentionally restores unchecked state; use one click rather than
                    # check(), which may retry a click while the controlled checkbox is replaced.
                    playback.click()
                    page.get_by_text("播放许可已在别处更新，请重新读取。", exact=True).first.wait_for()
                    page.locator(".douyin-web-playback-label input:not(:disabled)").wait_for()
                    assert get_count > before_gets, "保存冲突后应重新读取服务端许可"
                    assert not playback.is_checked(), "冲突不得把未保存的播放许可留在界面上"
                    playback.check()
                    page.get_by_text("已允许静音观察视频播放。", exact=True).wait_for()
                    scope.select_option("home")
                    page.get_by_text("已允许读取抖音首页网页文字。", exact=True).wait_for()
                    assert view["playback_enabled"] is False
                    assert playback.count() == 0, "降级为仅首页时应移除播放许可控件"
                    scope.select_option("home_and_videos")
                    playback.wait_for()
                    assert not playback.is_checked()
                    playback.check()
                    page.get_by_text("已允许静音观察视频播放。", exact=True).wait_for()
                    page.get_by_role("button", name="停止网页读取", exact=True).click()
                    page.get_by_role("button", name="允许读取首页文字", exact=True).wait_for()
                    assert view["playback_enabled"] is False and playback.count() == 0
                    page.get_by_role("button", name="允许读取首页文字", exact=True).click()
                    page.get_by_label("读取范围", exact=True).wait_for()
                    scope.select_option("home_and_videos")
                    playback.wait_for()
                    playback.check()
                    page.get_by_text("已允许静音观察视频播放。", exact=True).wait_for()
                    assert_no_horizontal_overflow(page, f"{width}-pixel Douyin playback permission")
                    assert page.locator("#app-dialog").evaluate("node => node.scrollTop") == 0, "应用弹窗外层不能随内部操作滚动并裁切标题"
                    page.locator("#app-dialog").screenshot(path=str(output / f"douyin-web-login-playback-{suffix}.png"))
                    before_gets = get_count
                    scope_conflict = True
                    scope.select_option("home")
                    page.get_by_text("读取范围已在别处更新，请重新读取。", exact=True).first.wait_for()
                    page.locator(".douyin-web-playback-label input:not(:disabled)").wait_for()
                    assert get_count > before_gets
                    assert page.get_by_label("读取范围", exact=True).input_value() == "home_and_videos", "失败后必须保留服务端读取范围"
                    assert playback.is_checked()
                    page.locator("#app-dialog").screenshot(path=str(output / f"douyin-web-login-read-{suffix}.png"))
                    # A saved profile can be opened again for a later re-login.
                    page.get_by_role("button", name="重新打开登录窗口", exact=True).click()
                    page.get_by_text("网页登录窗口已打开", exact=True).wait_for()
                    assert view["read_enabled"] is False, "重新登录必须撤销上一账号的首页文字读取许可"
                    assert view["read_scope"] == "home", "重新登录必须恢复默认读取范围"
                    assert view["playback_enabled"] is False, "重新登录必须撤销播放许可"
                    assert playback.count() == 0
                    view = {"revision": revision, "state": "unconfirmed", "active": False, "available": True, "attempt_id": "attempt-2", "read_enabled": False, "read_scope": "home"}
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
                    assert len(requests) == 20
                    assert not dialogs, "网页登录操作不得弹出浏览器原生确认框"
                    assert_no_horizontal_overflow(page, f"{width}-pixel Douyin web login")
                    page.locator("#app-dialog").screenshot(path=str(output / f"douyin-web-login-{suffix}.png"))

                    # A delayed 401 from a closed detail must not log the owner out or repaint stale state.
                    page.locator("#app-dialog-close").click()
                    late_401 = True
                    page.evaluate("window.fixtureDelayWebLogin = true")
                    page.get_by_role("button", name="管理抖音", exact=True).click()
                    page.locator("#app-dialog").wait_for(state="visible")
                    page.locator("#app-dialog-close").click()
                    page.evaluate("window.fixtureDelayWebLogin = false")
                    page.get_by_role("button", name="管理抖音", exact=True).click()
                    page.locator("#app-dialog").wait_for(state="visible")
                    page.wait_for_timeout(1100)
                    assert page.locator("#home-view").is_visible(), "late closed-detail 401 must not end the current session"
                    # Logging out also must not let a pending UI poll repopulate the old detail.
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
