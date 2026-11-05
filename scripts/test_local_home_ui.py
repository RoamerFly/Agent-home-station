"""Opt-in local home acceptance: synthetic data, real HTTP, zero model/WeChat calls."""

import argparse
import base64
import json
import os
from pathlib import Path
import queue
import re
import shutil
import subprocess
import tempfile
import threading

from playwright.sync_api import sync_playwright


def start(binary, data, workspace):
    environment = dict(os.environ, AHS_ADMIN_PASSWORD="synthetic-home-owner")
    process = subprocess.Popen(
        [str(binary), "--desktop", "-data", str(data), "-workspace", str(workspace), "-listen", "127.0.0.1:0"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment,
        creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0,
    )
    lines = queue.Queue()
    threading.Thread(target=lambda: lines.put(process.stdout.readline()), daemon=True).start()
    try:
        first = lines.get(timeout=30).decode("utf-8")
        endpoint = re.search(r"127\.0\.0\.1:\d+", first)
        if endpoint is None:
            raise RuntimeError("synthetic station did not report a loopback address")
        return process, "http://" + endpoint.group()
    except BaseException:
        process.terminate()
        process.wait(timeout=15)
        raise


def stop(process):
    if process.poll() is None:
        try:
            process.stdin.write(b"stop\n")
            process.stdin.flush()
            assert process.wait(timeout=15) == 0, "desktop service did not stop gracefully"
        except BaseException:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=15)
            raise
    process.stdin.close()
    process.stdout.close()
    process.stderr.close()


def assert_no_horizontal_overflow(page, scene):
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), f"{scene}: narrow overflow"


def app_connection_payload():
    """Synthetic registered application data for browser-only app status checks."""
    return {
        "items": [{"id": "wechat", "name": "微信", "registration": "station-native", "account_setup": True}],
    }


def wechat_connection_payload():
    return {
        "items": [{"id": "synthetic-wechat", "label": "合成微信账号", "primary": True}],
    }


def fulfill_json(payload, status=200):
    def handler(route):
        route.fulfill(status=status, content_type="application/json", body=json.dumps(payload, ensure_ascii=False))

    return handler


def verify_missing_source(binary, output):
    """A copied executable has no sibling connected-apps directory, by design."""
    with tempfile.TemporaryDirectory(prefix="synthetic-bare-station-", dir=output) as directory:
        directory = Path(directory).resolve()
        bare = directory / "bare" / binary.name
        bare.parent.mkdir()
        shutil.copy2(binary, bare)
        process, origin = start(bare, directory / "host", directory / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                context = browser.new_context(viewport={"width": 1280, "height": 800}, color_scheme="light")
                page = context.new_page()
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                page.locator("#chat-tab").focus()
                page.keyboard.press("ArrowRight")
                page.locator("#apps-view").wait_for(state="visible")
                assert page.evaluate("document.activeElement.id") == "apps-tab", "tab keyboard navigation"
                page.locator("#apps-list[aria-busy='false']").wait_for()
                page.get_by_text("未提供源码", exact=True).wait_for()
                assert page.get_by_role("button", name="安装源码").count() == 0
                page.screenshot(path=str(output / "apps-no-source-light.png"), full_page=True)
                browser.close()
        finally:
            stop(process)


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-home-", dir=output) as directory:
        directory = Path(directory).resolve()
        data, workspace = directory / "host", directory / "workspace"
        process, origin = start(binary, data, workspace)
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                context = browser.new_context(viewport={"width": 1280, "height": 800}, color_scheme="light")
                page = context.new_page()
                errors = []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.goto(origin)
                assert context.request.get(origin + "/api/local/runtime").status == 401
                page.locator("#login-view").wait_for(state="visible")
                page.screenshot(path=str(output / "login-light.png"))
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").wait_for(state="visible")
                assert page.locator("#login-view").is_hidden(), "login card remained visible after login"
                assert not (data / "fish-identity.json").exists(), "read created identity"
                runtime = context.request.get(origin + "/api/local/runtime").json()
                assert runtime["kind"] == "local-station" and runtime["pid"] == process.pid
                assert Path(runtime["data_directory"]).resolve() == data.resolve()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                initial = context.request.get(origin + "/api/local/home").json()
                assert initial["initialized"] and initial["home_access"]
                assert initial.get("chat_available") is True
                model = context.request.get(origin + "/api/local/model")
                assert model.status == 200
                model = model.json()
                assert model["provider"] == "deepseek"
                assert model["model"] == "deepseek-flash"
                assert model["thinking_enabled"] is True
                assert model["reasoning_effort"] == "low"
                assert model["key_configured"] is False
                page.locator("#chat-view").wait_for(state="visible")
                page.get_by_text("先设置聊天模型", exact=True).wait_for()
                assert page.locator("#chat-input").is_enabled(), "chat must remain available independently of workspace access"
                page.locator("#settings-tab").click()
                page.locator("#settings-view").wait_for(state="visible")
                assert page.locator("#model-save").is_visible()
                page.locator("#model-name").fill("synthetic-unsaved-draft")
                assert page.get_by_text("测试模型连接", exact=True).count() == 0
                page.locator("#chat-tab").click()
                home = Path(initial["directory"])
                assert home.is_relative_to(workspace)
                assert not (data / "accounts.json").exists()
                assert not (data / "companion").exists()
                assert context.request.get(origin + "/api/accounts").status == 404
                for name in ["Connected Apps", "Project Tools", "diary", "projects", "works", "scratch"]:
                    assert (home / name).is_dir(), name
                note = home / "diary" / "synthetic.md"
                note.write_text("a persistent companion home", encoding="utf-8")

                # The catalog and source checkout use the real local HTTP and CSRF path.
                # A plain server executable may intentionally ship without application source;
                # in that case this primary replay still verifies the useful empty-source state.
                page.locator("#chat-tab").focus()
                page.keyboard.press("ArrowRight")
                page.locator("#apps-view").wait_for(state="visible")
                assert page.evaluate("document.activeElement.id") == "apps-tab", "tab keyboard navigation"
                page.locator("#apps-list[aria-busy='false']").wait_for()
                source_state = page.locator("#apps-list").inner_text()
                catalog = context.request.get(origin + "/api/local/apps/catalog").json()["applications"]
                assert len(catalog) == 1 and catalog[0]["id"] == "wechat"
                source_available = catalog[0]["source_available"]
                if source_available:
                    assert "可安装" in source_state, source_state
                    page.get_by_role("button", name="安装源码").click()
                    page.get_by_text("源码已安装", exact=True).wait_for()
                    page.locator("#apps-feedback").get_by_text(re.compile("源码已.*家目录")).wait_for()
                    assert (home / "Connected Apps" / "wechat" / "app.json").is_file(), "installed application source"
                else:
                    assert "未提供源码" in source_state, source_state
                    assert page.get_by_role("button", name="安装源码").count() == 0

                # Application-account HTTP calls are intentionally routed in the browser only:
                # no login material, native connector, or actual WeChat request is involved.
                apps_route = fulfill_json(app_connection_payload())
                connections_route = fulfill_json(wechat_connection_payload())
                page.route("**/api/apps", apps_route)
                page.route("**/api/apps/wechat/connections", connections_route)
                status_pattern = "**/api/apps/wechat/connections/synthetic-wechat"
                status_route = fulfill_json({"connection": {"id": "synthetic-wechat", "login": "authenticated"}})
                page.route(status_pattern, status_route)
                page.locator("#apps-refresh").click()
                page.get_by_text("账号连接", exact=True).wait_for()
                page.get_by_text("合成微信账号", exact=True).wait_for()
                page.get_by_text("已登录", exact=True).wait_for()
                page.screenshot(path=str(output / "apps-connected-light.png"), full_page=True)

                # Keep the same request key when the first response is lost,
                # then display the exact operation generation returned by status.
                connection_base = "**/api/apps/wechat/connections"
                login_pattern = status_pattern + "/login"
                operation_pattern = login_pattern + "/fixture-operation"
                qr_pattern = operation_pattern + "/qr?generation=2"
                requests = {"provision": [], "login": [], "qr": 0}
                progress = {"provisioned": False, "state": "awaiting_scan"}

                def setup_connections(route):
                    if route.request.method == "POST":
                        requests["provision"].append(route.request.post_data_json)
                        progress["provisioned"] = True
                        fulfill_json({"id": "synthetic-wechat", "account_id": "fixture"})(route)
                    else:
                        fulfill_json(wechat_connection_payload() if progress["provisioned"] else {"items": []})(route)

                def setup_start(route):
                    requests["login"].append(route.request.post_data_json)
                    if len(requests["login"]) == 1:
                        fulfill_json({"error": "暂时无法核对账号操作"}, status=503)(route)
                    else:
                        fulfill_json({"id": "fixture-operation", "state": "starting"})(route)

                def setup_operation(route):
                    state = progress["state"]
                    fulfill_json({"id": "fixture-operation", "state": state, "qr_available": state == "awaiting_scan", "qr_generation": 2})(route)

                def setup_qr(route):
                    requests["qr"] += 1
                    pixel = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=")
                    route.fulfill(content_type="image/png", body=pixel)

                page.unroute(connection_base, connections_route)
                page.unroute(status_pattern, status_route)
                status_route = fulfill_json({"connection": {"id": "synthetic-wechat", "login": "signed_out", "runtime": "offline"}})
                page.route(connection_base, setup_connections)
                page.route(status_pattern, status_route)
                page.route(login_pattern, setup_start)
                page.route(operation_pattern, setup_operation)
                page.route(qr_pattern, setup_qr)
                page.locator("#apps-refresh").click()
                page.get_by_text("账号未接入", exact=True).wait_for()
                page.get_by_role("button", name="添加账号").click()
                page.get_by_role("button", name="扫码登录").click()
                page.get_by_text("暂时无法核对账号操作", exact=True).wait_for()
                page.get_by_role("button", name="扫码登录").click()
                page.get_by_alt_text("微信登录二维码").wait_for()
                assert requests["provision"] and requests["login"][0] == requests["login"][1]
                page.wait_for_function("document.querySelector('.app-login img')?.naturalWidth > 0")
                assert requests["qr"] == 1
                page.screenshot(path=str(output / "apps-synthetic-qr-light.png"), full_page=True)
                progress["state"] = "expired"
                page.get_by_text("二维码已过期", exact=True).wait_for()
                assert page.get_by_alt_text("微信登录二维码").count() == 0
                page.unroute(login_pattern, setup_start)
                page.unroute(operation_pattern, setup_operation)
                page.unroute(qr_pattern, setup_qr)

                # An unavailable account read remains a visible, recoverable application state.
                page.unroute(connection_base, setup_connections)
                unavailable_route = fulfill_json({"error": "synthetic connection unavailable"}, status=503)
                page.route("**/api/apps/wechat/connections", unavailable_route)
                page.locator("#apps-refresh").click()
                page.get_by_text("账号状态暂不可读", exact=True).wait_for()
                page.unroute("**/api/apps/wechat/connections", unavailable_route)
                page.unroute("**/api/apps", apps_route)
                page.unroute(status_pattern, status_route)

                page.locator("#settings-tab").click()
                assert page.locator("#model-name").input_value() == "synthetic-unsaved-draft", "application state replaced the model draft"
                page.locator("#chat-tab").click()
                page.screenshot(path=str(output / "ready-light.png"), full_page=True)
                page.locator("#settings-tab").click()
                page.locator("#settings-access-button").click()
                page.get_by_text("工作空间已暂停", exact=True).wait_for()
                assert page.locator("#model-name").input_value() == "synthetic-unsaved-draft", "workspace change replaced the model draft"
                page.locator("#chat-tab").click()
                assert page.locator("#chat-input").is_enabled(), "workspace pause must not disable local chat"
                page.reload()
                page.get_by_text("工作空间已暂停", exact=True).wait_for()
                session = context.request.get(origin + "/api/session").json()
                stale = context.request.post(origin + "/api/local/home", data={"action": "authorize", "revision": initial["revision"]}, headers={"Origin": origin, "X-CSRF-Token": session["csrf"]})
                assert stale.status == 409, "stale revision revived grant"
                page.set_viewport_size({"width": 375, "height": 812})
                page.emulate_media(color_scheme="dark")
                assert_no_horizontal_overflow(page, "settings")
                page.screenshot(path=str(output / "paused-dark-375.png"), full_page=True)
                page.get_by_role("tab", name="应用").click()
                page.locator("#apps-view").wait_for(state="visible")
                assert_no_horizontal_overflow(page, "apps")
                page.screenshot(path=str(output / "apps-dark-375.png"), full_page=True)
                page.locator("#settings-tab").click()
                page.locator("#settings-access-button").click()
                page.get_by_text("工作空间已授权", exact=True).wait_for()
                page.locator("#settings-logout-button").click()
                page.locator("#login-view").wait_for(state="visible")
                assert context.request.get(origin + "/api/local/home").status == 401
                stop(process)
                process, origin = start(binary, data, workspace)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#chat-view").wait_for(state="visible")
                latest = context.request.get(origin + "/api/local/home").json()
                assert latest["fish_id"] == initial["fish_id"]
                assert latest["owner_id"] == initial["owner_id"]
                assert latest["directory"] == initial["directory"]
                assert note.read_text(encoding="utf-8") == "a persistent companion home"
                assert not errors, errors
                browser.close()
                print(json.dumps({"status": "passed", "model_calls": 0, "wechat_calls": 0, "scenes": ["login", "initialize", "application catalog", "application source install or unavailable source", "synthetic application account", "synthetic QR retry and expiry", "application account unavailable", "pause", "stale revision", "resume", "logout", "restart", "375 dark"]}, ensure_ascii=False))
        finally:
            stop(process)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--bare-binary", type=Path, help="optional executable without a sibling connected-apps directory")
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
    verify_missing_source((args.bare_binary or args.binary).resolve(), args.output.resolve())
