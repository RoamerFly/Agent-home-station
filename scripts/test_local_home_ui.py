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
from urllib.parse import parse_qs, unquote, urlparse

from playwright.sync_api import expect, sync_playwright


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


def assert_worker_controls_fit(page):
    clipped = page.evaluate("""() => {
        const panel = document.querySelector('.app-worker');
        const area = panel.getBoundingClientRect();
        return [...panel.querySelectorAll('button, textarea, input, .delivery-recall select')].filter((node) => {
            if (!node.getClientRects().length || node.closest('details:not([open])')) return false;
            const bounds = node.getBoundingClientRect();
            return bounds.left < area.left - 1 || bounds.right > area.right + 1;
        }).map((node) => node.getAttribute('aria-label') || node.textContent.trim());
    }""")
    assert not clipped, f"visible worker controls clipped inside their card: {clipped}"


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


def project_tool_payload(installed=False, enabled=False, revision=1, state="idle", pdf_ready=False, update_available=False):
    return {"tools": [{
        "id": "document-workshop", "name": "文档工坊", "catalog_available": True,
        "runtime_ready": True, "installed": installed, "enabled": enabled,
        "revision": revision, "state": state, "pdf_ready": pdf_ready,
        "update_available": update_available,
    }]}


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

                # The project-tool endpoint is a browser-only route fixture. It
                # validates explicit artifact approval and exercises install,
                # enable, disable, and an artifact-preserving update without
                # starting a real tool process.
                tool_state = {"installed": False, "enabled": False, "revision": 1, "state": "idle", "pdf_ready": False, "update_available": False}
                project_tools_pattern = re.compile(r".*/api/local/project-tools(?:/document-workshop)?$")

                def project_tools_route(route):
                    request = route.request
                    if request.method == "GET":
                        route.fulfill(content_type="application/json", body=json.dumps(project_tool_payload(**tool_state), ensure_ascii=False))
                        return
                    assert request.method == "PUT"
                    body = request.post_data_json
                    assert body["revision"] == tool_state["revision"]
                    if body["action"] == "install":
                        assert body["approve_artifacts"] is True
                        tool_state.update(installed=True, enabled=False, revision=2)
                    elif body["action"] == "enable":
                        assert body["approve_artifacts"] is False
                        tool_state.update(enabled=True, revision=3, state="idle")
                    elif body["action"] == "disable":
                        assert body["approve_artifacts"] is False
                        tool_state.update(enabled=False, revision=4, state="idle", update_available=True)
                    elif body["action"] == "upgrade":
                        assert tool_state["enabled"] is False
                        assert body["approve_artifacts"] is True
                        tool_state.update(revision=5, state="idle", pdf_ready=True, update_available=False)
                    else:
                        raise AssertionError("unexpected project-tool action")
                    route.fulfill(content_type="application/json", body=json.dumps({"tool": project_tool_payload(**tool_state)["tools"][0]}, ensure_ascii=False))

                page.route(project_tools_pattern, project_tools_route)
                page.get_by_role("tab", name="项目工具", exact=True).click()
                page.locator("#project-tools-view").wait_for(state="visible")
                page.get_by_text("文档工坊", exact=True).wait_for()
                install = page.get_by_role("button", name="安装", exact=True)
                assert install.is_disabled(), "artifact confirmation must gate installation"
                page.get_by_role("checkbox", name="确认本机素材读写", exact=True).check()
                install.click()
                page.get_by_text("文档工坊已安装。", exact=True).wait_for()
                page.get_by_role("button", name="启用", exact=True).click()
                page.get_by_text("文档工坊已启用。", exact=True).wait_for()
                page.get_by_text("已启用，待命", exact=True).wait_for()
                page.get_by_role("button", name="停用", exact=True).click()
                page.get_by_text("文档工坊已停用。", exact=True).wait_for()
                page.get_by_text("已安装，可更新", exact=True).wait_for()
                upgrade = page.get_by_role("button", name="更新", exact=True)
                assert upgrade.is_disabled(), "artifact confirmation must gate project-tool update"
                page.get_by_role("checkbox", name="确认本机素材读写", exact=True).check()
                upgrade.click()
                page.get_by_text("文档工坊已更新。", exact=True).wait_for()
                page.get_by_text("PDF 导出环境已就绪。", exact=True).wait_for()
                page.screenshot(path=str(output / "project-tools-light.png"), full_page=True)
                page.unroute(project_tools_pattern, project_tools_route)

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
                assert page.get_by_text("从旧版接入", exact=True).count() == 0, "legacy import stays hidden without an account flag"
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

                worker_policy_pattern = status_pattern + "/policy"
                worker_lifecycle_pattern = status_pattern + "/lifecycle"
                worker = {"state": "disabled", "threads": [], "history_threads": [], "attachment_threads": [], "media_reply_threads": [], "group_mute_threads": [], "group_rules": [], "contact_read": False, "actions": [], "policy_payloads": []}
                legacy = {"availability": "available", "previews": 0, "posts": []}
                legacy_memory = {"requests": []}
                legacy_memory_selection = {"gets": [], "posts": []}

                def legacy_plan(applied=False):
                    refreshed = legacy["availability"] == "applied" and legacy["previews"] >= 3
                    cutover = ({"legacy_host": "changed", "native_profile": "pending", "target_login": "online",
                                "target_identity": "owner_check_pending"} if refreshed else
                               {"legacy_host": "stop_record_matched", "native_profile": "import_receipt_matched",
                                "target_login": "signed_out", "target_identity": "owner_check_pending"})
                    return {"digest": f"fixture-legacy-digest-{legacy['previews']}", "account_id": "fixture-account",
                            "suggested_reply_threads": ["synthetic-room@chatroom"],
                            "requires_manual_review": ["all_friends"],
                            "receipt_records": 3, "unresolved_receipts": 1,
                            "same_native_dataset": True, "legacy_enabled": True,
                            "legacy_paused": True, "applied": applied, "cutover": cutover}

                def worker_view():
                    connection = {"id": "synthetic-wechat", "login": "authenticated",
                        "runtime": "online", "worker": worker["state"], "configured_policy": {
                            "conversation_enabled": bool(worker["threads"]), "reply_threads": worker["threads"], "history_threads": worker["history_threads"],
                            "attachment_threads": worker["attachment_threads"], "media_reply_threads": worker["media_reply_threads"],
                            "group_mute_threads": worker["group_mute_threads"],
                            "group_rules": worker["group_rules"],
                            "contact_read": worker["contact_read"]}}
                    if legacy["availability"]:
                        connection["legacy_import"] = legacy["availability"]
                    return {"connection": connection}

                def read_worker(route):
                    fulfill_json(worker_view())(route)

                def save_worker(route):
                    assert route.request.method == "PUT"
                    body = route.request.post_data_json
                    assert set(body["media_reply_threads"]) <= set(body["attachment_threads"]), "media reply cannot survive attachment-read revocation"
                    worker["policy_payloads"].append(body)
                    worker["threads"] = body["reply_threads"]
                    worker["history_threads"] = body["history_threads"]
                    worker["attachment_threads"] = body["attachment_threads"]
                    worker["media_reply_threads"] = body["media_reply_threads"]
                    worker["group_mute_threads"] = body["group_mute_threads"]
                    worker["group_rules"] = body.get("group_rules", [])
                    worker["contact_read"] = body["contact_read"]
                    fulfill_json(worker_view())(route)

                def set_worker(route):
                    assert route.request.method == "POST"
                    action = route.request.post_data_json["action"]
                    worker["actions"].append(action)
                    worker["state"] = "running" if action == "start" else "paused"
                    fulfill_json({"worker": worker["state"]})(route)

                page.unroute(status_pattern, status_route)
                status_route = read_worker
                page.route(status_pattern, status_route)
                page.route(worker_policy_pattern, save_worker)
                page.route(worker_lifecycle_pattern, set_worker)
                legacy_import_pattern = status_pattern + "/legacy-import"

                def legacy_import(route):
                    if route.request.method == "GET":
                        legacy["previews"] += 1
                        fulfill_json(legacy_plan(legacy["availability"] == "applied"))(route)
                        return
                    assert route.request.method == "POST"
                    legacy["posts"].append(route.request.post_data_json)
                    assert route.request.headers.get("x-csrf-token"), "legacy import lacks owner CSRF"
                    assert route.request.post_data_json == {"digest": f"fixture-legacy-digest-{legacy['previews']}"}
                    if len(legacy["posts"]) == 1:
                        fulfill_json({"error": "stale legacy preview"}, status=409)(route)
                        return
                    legacy["availability"] = "applied"
                    fulfill_json(legacy_plan(True))(route)

                page.route(legacy_import_pattern, legacy_import)
                legacy_memory_pattern = legacy_import_pattern + "/memory*"

                def legacy_memory_report(route):
                    path = urlparse(route.request.url).path
                    selected_prefix = "/memory/"
                    if selected_prefix in path:
                        thread = unquote(path.split(selected_prefix, 1)[1])
                        assert thread == "synthetic-room@chatroom"
                        if route.request.method == "GET":
                            legacy_memory_selection["gets"].append(thread)
                            digest = "fixture-memory-selection-v2" if len(legacy_memory_selection["gets"]) > 1 else "fixture-memory-selection-v1"
                            fulfill_json({"status": "ready", "digest": digest, "event_count": 12,
                                          "memory_count": 3, "reason": "当前 Agent 已暂停，接续不会自动启动"})(route)
                            return
                        assert route.request.method == "POST"
                        assert route.request.headers.get("x-csrf-token"), "memory continuation lacks owner CSRF"
                        payload = route.request.post_data_json
                        legacy_memory_selection["posts"].append(payload)
                        assert payload.get("same_identity") is True
                        if len(legacy_memory_selection["posts"]) == 1:
                            assert payload == {"digest": "fixture-memory-selection-v1", "same_identity": True}
                            fulfill_json({"error": "stale memory preview"}, status=409)(route)
                            return
                        assert payload == {"digest": "fixture-memory-selection-v2", "same_identity": True}
                        fulfill_json({"status": "applied", "event_count": 12, "memory_count": 3,
                                      "reason": "已接续到当前账号的记忆库"})(route)
                        return
                    assert route.request.method == "GET"
                    query = parse_qs(urlparse(route.request.url).query, keep_blank_values=True)
                    after = query.get("after", [""])[0]
                    legacy_memory["requests"].append(after)
                    if after == "fixture-memory-next":
                        fulfill_json({"status": "ready", "matched_scopes": 2, "unmapped_scopes": 1, "foreign_scopes": 0,
                                      "conversations": [{"thread_id": "synthetic-room-older@chatroom", "event_count": 4,
                                                         "withheld_events": 1, "memory_count": 1, "has_summary": False,
                                                         "history_granted": False}]})(route)
                        return
                    if len([item for item in legacy_memory["requests"] if not item]) > 1:
                        fulfill_json({"status": "snapshot_incomplete", "matched_scopes": 2, "unmapped_scopes": 1,
                                      "foreign_scopes": 0, "conversations": []})(route)
                        return
                    fulfill_json({"status": "ready", "matched_scopes": 2, "unmapped_scopes": 1, "foreign_scopes": 0,
                                  "conversations": [{"thread_id": "synthetic-room@chatroom", "event_count": 12,
                                                     "withheld_events": 2, "memory_count": 3, "has_summary": True,
                                                     "history_granted": True}], "next_after": "fixture-memory-next"})(route)

                page.route(legacy_memory_pattern, legacy_memory_report)
                # Register the per-conversation route after the inventory route.
                # Playwright runs matching handlers in reverse registration order,
                # so this keeps a selected scope out of the paged inventory handler.
                page.route(legacy_import_pattern + "/memory/*", legacy_memory_report)
                contacts_pattern = status_pattern + "/contacts*"
                contact_queries = []

                def contact_directory(route):
                    assert route.request.method == "GET"
                    assert "/synthetic-wechat/contacts" in route.request.url
                    query = parse_qs(urlparse(route.request.url).query, keep_blank_values=True)
                    contact_queries.append(query)
                    assert query["search"] == ["蓝"]
                    if query["limit"] == ["500"]:
                        assert "cursor" not in query
                        fulfill_json({"items": [{"id": "wxid-synthetic-4", "display_name": "大页联系人"}], "has_more": False,
                                      "effective_limit": 200, "limit_reason": "原生接口单页上限为 200 条，可用续读位置继续。",
                                      "coverage": "已读取当前连接的本机通讯录快照"})(route)
                        return
                    assert query["limit"] == ["50"]
                    if query.get("cursor") == ["synthetic-next"]:
                        fulfill_json({"items": [{"id": "wxid-synthetic-3", "display_name": "蓝鲸"}], "has_more": False, "effective_limit": 50,
                                      "coverage": "已读取 3 位匹配联系人"})(route)
                    else:
                        assert "cursor" not in query
                        fulfill_json({"items": [{"id": "wxid-synthetic-1", "display_name": "蓝色大肥鱼"},
                                                {"id": "wxid-synthetic-2", "display_name": "蓝朋友"}],
                                      "next_cursor": "synthetic-next", "has_more": True,
                                      "effective_limit": 50,
                                      "coverage": "已读取前 2 位匹配联系人"})(route)

                page.route(contacts_pattern, contact_directory)
                page.locator("#apps-refresh").click()
                page.get_by_text("Agent 未启动", exact=True).wait_for()
                assert page.get_by_role("button", name="启动 Agent").is_disabled()
                page.get_by_text("从旧版接入", exact=True).click()
                page.get_by_role("button", name="查看接入预演", exact=True).click()
                page.get_by_text("接入预演", exact=True).wait_for()
                page.get_by_text("账号：fixture-account", exact=True).wait_for()
                page.get_by_text("建议回复范围：synthetic-room@chatroom", exact=True).wait_for()
                page.get_by_text("仍需核对：旧版允许所有好友回复", exact=True).wait_for()
                page.get_by_text("旧宿主：已记录正常停机", exact=True).wait_for()
                page.get_by_text("原生档案：导入记录与当前连接匹配", exact=True).wait_for()
                page.get_by_text("目标登录：尚未登录", exact=True).wait_for()
                page.get_by_text("身份确认：仍需主人确认实际微信身份", exact=True).wait_for()
                assert legacy["previews"] == 1 and not legacy["posts"] and not legacy_memory["requests"] and not worker["actions"] and not worker["threads"], "preview must be read-only"
                page.get_by_role("button", name="核对旧会话记忆", exact=True).click()
                page.get_by_text("旧会话记忆可核对", exact=True).wait_for()
                page.get_by_text("匹配范围：2；未映射范围：1；外部范围：0", exact=True).wait_for()
                page.get_by_text("会话：synthetic-room@chatroom；来源候选：12；已排除：2；记忆：3；摘要：有；当前目标历史授权：已授权", exact=True).wait_for()
                page.get_by_text("历史授权仅表示当前目标连接的读取权限，不代表可读取旧版资料。", exact=True).wait_for()
                page.get_by_role("button", name="预演接续此会话", exact=True).click()
                page.get_by_text("可接续此会话", exact=True).wait_for()
                page.get_by_text("来源事件：12；可接续记忆：3", exact=True).wait_for()
                page.get_by_text("当前 Agent：Agent 未启动；已配置历史读取会话：0 个。", exact=True).wait_for()
                page.get_by_text("说明：当前 Agent 已暂停，接续不会自动启动", exact=True).wait_for()
                memory_apply = page.get_by_role("button", name="确认接续此会话", exact=True)
                assert memory_apply.is_disabled(), "memory continuation requires explicit identity confirmation"
                assert not legacy_memory_selection["posts"], "memory preview must not write before confirmation"
                page.get_by_label("我已确认旧资料和当前目标是同一个实际微信账号", exact=True).check()
                memory_apply.click()
                page.get_by_text("旧资料或当前目标已变化，请重新预演此会话。", exact=True).wait_for()
                assert legacy_memory_selection["posts"] == [{"digest": "fixture-memory-selection-v1", "same_identity": True}]
                page.get_by_role("button", name="重新预演此会话", exact=True).click()
                page.get_by_label("我已确认旧资料和当前目标是同一个实际微信账号", exact=True).check()
                page.get_by_role("button", name="确认接续此会话", exact=True).click()
                page.get_by_text("此会话已接续", exact=True).wait_for()
                assert legacy_memory_selection["posts"] == [
                    {"digest": "fixture-memory-selection-v1", "same_identity": True},
                    {"digest": "fixture-memory-selection-v2", "same_identity": True}
                ], "memory continuation must reread after stale confirmation"
                page.get_by_role("button", name="读取下一页会话", exact=True).click()
                page.get_by_text("会话：synthetic-room-older@chatroom；来源候选：4；已排除：1；记忆：1；摘要：无；当前目标历史授权：未授权", exact=True).wait_for()
                assert legacy_memory["requests"] == ["", "fixture-memory-next"] and not legacy["posts"], "memory check must be read-only"
                page.get_by_role("button", name="核对旧会话记忆", exact=True).click()
                page.get_by_text("旧会话快照不完整，请使用正常关闭后的一致副本", exact=True).wait_for()
                assert legacy_memory["requests"] == ["", "fixture-memory-next", ""] and not legacy["posts"], "unavailable memory state must remain read-only"
                page.get_by_role("button", name="确认接入旧版摘要", exact=True).click()
                page.get_by_text("旧版资料或当前状态已变化，请重新读取接入预演。", exact=True).wait_for()
                assert page.get_by_role("button", name="确认接入旧版摘要", exact=True).count() == 0, "stale preview requires a fresh read"
                page.get_by_role("button", name="查看接入预演", exact=True).click()
                page.get_by_role("button", name="确认接入旧版摘要", exact=True).click()
                page.get_by_text("旧版摘要已保存；没有启动 Agent，也没有修改回复范围。", exact=True).wait_for()
                assert legacy["previews"] == 2 and legacy["posts"] == [{"digest": "fixture-legacy-digest-1"}, {"digest": "fixture-legacy-digest-2"}]
                assert not worker["actions"] and not worker["threads"], "legacy import must not mutate lifecycle or reply policy"
                page.get_by_role("button", name="查看接入摘要", exact=True).wait_for()
                page.get_by_role("button", name="查看接入摘要", exact=True).click()
                page.get_by_text("旧宿主：停机记录与当前快照不一致", exact=True).wait_for()
                page.get_by_text("原生档案：尚未取得档案导入记录", exact=True).wait_for()
                page.get_by_text("目标登录：已登录且在线", exact=True).wait_for()
                page.get_by_text("身份确认：仍需主人确认实际微信身份", exact=True).wait_for()
                assert legacy["previews"] == 3 and len(legacy["posts"]) == 2, "cutover refresh must remain read-only"
                page.get_by_role("button", name="设置回复范围").click()
                page.get_by_role("textbox", name="回复会话 ID", exact=True).fill("filehelper\nsynthetic-room@chatroom")
                page.get_by_role("textbox", name="历史与成员读取会话 ID", exact=True).fill("synthetic-room@chatroom")
                summary_share = page.get_by_role("checkbox", name="synthetic-room@chatroom 自动总结视频号分享", exact=True)
                assert summary_share.is_disabled(), "share summaries require a same-group attachment-read grant"
                page.get_by_role("textbox", name="附件读取会话 ID").fill("synthetic-room@chatroom")
                summary_share = page.get_by_role("checkbox", name="synthetic-room@chatroom 自动总结视频号分享", exact=True)
                assert summary_share.is_enabled(), "attachment-read grant should unlock only this group's summary switch"
                page.get_by_role("textbox", name="synthetic-room@chatroom 群聊备注", exact=True).fill("收到分享时先总结重点")
                page.locator("#apps-refresh").click()
                page.locator("#apps-list[aria-busy='true']").wait_for()
                page.locator("#apps-list[aria-busy='false']").wait_for()
                expect(page.get_by_role("textbox", name="synthetic-room@chatroom 群聊备注", exact=True)).to_have_value("收到分享时先总结重点")
                page.get_by_role("checkbox", name="synthetic-room@chatroom 自动总结视频号分享", exact=True).check()
                page.get_by_role("textbox", name="附件回复会话 ID").fill("synthetic-room@chatroom")
                page.get_by_role("textbox", name="Agent 免打扰群 ID", exact=True).fill("synthetic-mute@chatroom")
                page.get_by_role("checkbox", name="允许大肥鱼在本地聊天中查阅此通讯录").check()
                page.get_by_role("button", name="保存回复范围").click()
                page.locator("#apps-feedback").get_by_text("回复范围已保存。", exact=True).wait_for()
                page.get_by_role("button", name="启动 Agent").click()
                page.get_by_text("Agent 运行中", exact=True).wait_for()
                page.get_by_role("button", name="暂停 Agent").click()
                page.get_by_text("Agent 已暂停", exact=True).wait_for()
                assert worker["threads"] == ["filehelper", "synthetic-room@chatroom"]
                assert worker["history_threads"] == ["synthetic-room@chatroom"]
                assert page.get_by_role("textbox", name="历史与成员读取会话 ID", exact=True).input_value() == "synthetic-room@chatroom"
                assert worker["attachment_threads"] == ["synthetic-room@chatroom"]
                assert worker["group_rules"] == [{"thread_id": "synthetic-room@chatroom", "note": "收到分享时先总结重点", "summarize_shares": True}]
                assert worker["policy_payloads"][-1]["group_rules"] == worker["group_rules"], "group settings must use the local policy route only"
                assert page.get_by_role("textbox", name="附件读取会话 ID").input_value() == "synthetic-room@chatroom"
                assert worker["media_reply_threads"] == ["synthetic-room@chatroom"]
                assert page.get_by_role("textbox", name="附件回复会话 ID").input_value() == "synthetic-room@chatroom"
                assert worker["group_mute_threads"] == ["synthetic-mute@chatroom"]
                assert page.get_by_role("textbox", name="Agent 免打扰群 ID", exact=True).input_value() == "synthetic-mute@chatroom"
                assert worker["contact_read"] is True
                assert worker["actions"] == ["start", "stop"]
                page.get_by_role("textbox", name="附件读取会话 ID").fill("")
                assert page.get_by_role("checkbox", name="synthetic-room@chatroom 自动总结视频号分享", exact=True).is_disabled()
                page.get_by_role("button", name="保存回复范围").click()
                page.locator("#apps-feedback").get_by_text("回复范围已保存。", exact=True).wait_for()
                assert worker["attachment_threads"] == [] and worker["media_reply_threads"] == []
                assert worker["group_rules"] == [{"thread_id": "synthetic-room@chatroom", "note": "收到分享时先总结重点", "summarize_shares": False}]
                page.get_by_role("textbox", name="Agent 免打扰群 ID", exact=True).fill("")
                page.get_by_role("button", name="保存回复范围").click()
                page.locator("#apps-feedback").get_by_text("回复范围已保存。", exact=True).wait_for()
                assert worker["group_mute_threads"] == []
                expect(page.get_by_role("textbox", name="Agent 免打扰群 ID", exact=True)).to_have_value("")
                assert page.get_by_role("textbox", name="Agent 免打扰群 ID", exact=True).input_value() == ""
                page.get_by_text("通讯录", exact=True).click()
                page.get_by_role("textbox", name="搜索联系人", exact=True).fill("蓝")
                page.get_by_role("button", name="查询联系人", exact=True).click()
                page.get_by_text("蓝色大肥鱼 · wxid-synthetic-1", exact=True).wait_for()
                page.get_by_text("已读取前 2 位匹配联系人 · 本页 2 条，每页最多 50 条", exact=True).wait_for()
                page.get_by_role("textbox", name="搜索联系人", exact=True).fill("不应影响下一页")
                page.get_by_role("spinbutton", name="联系人每页数量", exact=True).fill("99")
                page.get_by_role("button", name="下一页", exact=True).click()
                page.get_by_text("蓝鲸 · wxid-synthetic-3", exact=True).wait_for()
                assert page.get_by_text("蓝色大肥鱼 · wxid-synthetic-1", exact=True).count() == 0
                page.get_by_role("textbox", name="搜索联系人", exact=True).fill("蓝")
                page.get_by_role("spinbutton", name="联系人每页数量", exact=True).fill("500")
                page.get_by_role("button", name="查询联系人", exact=True).click()
                page.get_by_text("大页联系人 · wxid-synthetic-4", exact=True).wait_for()
                page.get_by_text("本页 1 条，每页最多 200 条 · 原生接口单页上限为 200 条，可用续读位置继续。", exact=False).wait_for()
                assert len(contact_queries) == 3

                groups_pattern = re.compile(r".*/api/apps/wechat/connections/synthetic-wechat/groups(?:\?.*)?$")
                group_notification_pattern = re.compile(r".*/api/apps/wechat/connections/synthetic-wechat/groups/.+/notification$")
                group_profile_pattern = re.compile(r".*/api/apps/wechat/connections/synthetic-wechat/groups/.+/profile$")
                group_actions_pattern = status_pattern + "/group-actions"
                group_queries, group_actions, group_reads = [], [], {}
                group_state = {"muted": False, "operation": None, "name": "合成测试群", "rename_operation": None,
                               "my_display_name": "合成群昵称", "my_name_operation": None}

                def group_directory(route):
                    assert route.request.method == "GET"
                    assert "/synthetic-wechat/groups" in route.request.url
                    query = parse_qs(urlparse(route.request.url).query, keep_blank_values=True)
                    group_queries.append(query)
                    assert query["search"] == ["测试"]
                    assert query["limit"] == ["50"]
                    if query.get("cursor") == ["synthetic-groups-next"]:
                        fulfill_json({"items": [{"id": "unknown-room@chatroom", "display_name": "未知状态群"}], "has_more": False,
                                      "effective_limit": 50, "coverage": "已读取 2 个匹配群聊"})(route)
                    else:
                        assert "cursor" not in query
                        fulfill_json({"items": [{"id": "synthetic-room@chatroom", "display_name": "合成测试群"}],
                                      "next_cursor": "synthetic-groups-next", "has_more": True, "effective_limit": 50,
                                      "coverage": "已读取前 1 个匹配群聊"})(route)

                def group_notification(route):
                    assert route.request.method == "GET"
                    path = unquote(urlparse(route.request.url).path)
                    group_id = path.split("/groups/", 1)[1].rsplit("/notification", 1)[0]
                    group_reads[group_id] = group_reads.get(group_id, 0) + 1
                    if group_id == "synthetic-room@chatroom":
                        if group_reads[group_id] >= 2 and group_state["operation"]:
                            group_state.update(muted=True, operation={"operation_id": "group-mute-1", "state": "succeeded", "lookup": "wechat-receipt"})
                        fulfill_json({"muted": group_state["muted"], "can_change": True, "operation": group_state["operation"]})(route)
                        return
                    assert group_id == "unknown-room@chatroom"
                    fulfill_json({"can_change": False, "coverage": "unavailable",
                                  "operation": {"operation_id": "group-mute-unknown", "state": "submission_unknown", "lookup": "native-lookup"}})(route)

                def group_profile(route):
                    assert route.request.method == "GET"
                    path = unquote(urlparse(route.request.url).path)
                    group_id = path.split("/groups/", 1)[1].rsplit("/profile", 1)[0]
                    if group_id == "synthetic-room@chatroom":
                        if group_state["rename_operation"] and group_state["rename_operation"]["state"] == "submitted_pending":
                            group_state.update(name="新的合成群名", rename_operation={"operation_id": "group-rename-1", "state": "succeeded", "lookup": "wechat-receipt"})
                        if group_state["my_name_operation"] and group_state["my_name_operation"]["state"] == "submitted_pending":
                            group_state.update(my_display_name="", my_name_operation={"operation_id": "group-my-name-1", "state": "succeeded", "lookup": "wechat-receipt"})
                        fulfill_json({"name": group_state["name"], "can_rename": True, "coverage": "cached", "operation": group_state["rename_operation"],
                                      "my_display_name": group_state["my_display_name"], "can_set_my_name": True,
                                      "my_name_operation": group_state["my_name_operation"]})(route)
                        return
                    assert group_id == "unknown-room@chatroom"
                    fulfill_json({"can_rename": False, "can_set_my_name": False, "coverage": "unavailable",
                                  "operation": {"operation_id": "group-rename-unknown", "state": "submission_unknown", "lookup": "native-lookup"},
                                  "my_name_operation": {"operation_id": "group-my-name-unknown", "state": "submission_unknown", "lookup": "native-lookup"}})(route)

                def group_action(route):
                    assert route.request.method == "POST"
                    assert route.request.headers.get("x-csrf-token")
                    body = route.request.post_data_json
                    assert body["group_id"] == "synthetic-room@chatroom"
                    assert re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", body["request_key"])
                    group_actions.append(body)
                    if body.get("action") == "rename":
                        assert body == {"action": "rename", "group_id": "synthetic-room@chatroom", "name": "新的合成群名", "request_key": body["request_key"]}
                        group_state["rename_operation"] = {"operation_id": "group-rename-1", "state": "submitted_pending", "lookup": "native-queued"}
                        fulfill_json(group_state["rename_operation"])(route)
                        return
                    if body.get("action") == "my_name":
                        assert body == {"action": "my_name", "group_id": "synthetic-room@chatroom", "name": "", "request_key": body["request_key"]}
                        group_state["my_name_operation"] = {"operation_id": "group-my-name-1", "state": "submitted_pending", "lookup": "native-queued"}
                        fulfill_json(group_state["my_name_operation"])(route)
                        return
                    assert body["muted"] is True
                    group_state["operation"] = {"operation_id": "group-mute-1", "state": "submitted_pending", "lookup": "native-queued"}
                    fulfill_json(group_state["operation"])(route)

                page.route(groups_pattern, group_directory)
                page.route(group_notification_pattern, group_notification)
                page.route(group_profile_pattern, group_profile)
                page.route(group_actions_pattern, group_action)
                page.get_by_text("群聊", exact=True).click()
                page.get_by_role("textbox", name="搜索群聊", exact=True).fill("测试")
                page.get_by_role("button", name="查询群聊", exact=True).click()
                page.get_by_role("button", name="合成测试群 · synthetic-room@chatroom", exact=True).click()
                page.get_by_text("当前为正常提醒", exact=True).wait_for()
                page.get_by_role("button", name="设为免打扰", exact=True).click()
                page.get_by_text("将“合成测试群”设为免打扰。", exact=True).wait_for()
                page.get_by_role("button", name="确认提交", exact=True).click()
                page.get_by_text("已提交，等待微信确认 · 操作 group-mute-1", exact=True).wait_for()
                assert len(group_actions) == 1
                page.get_by_role("button", name="刷新状态", exact=True).click()
                page.get_by_text("当前为免打扰", exact=True).wait_for()
                page.get_by_text("已确认完成 · 操作 group-mute-1", exact=True).wait_for()
                assert len(group_actions) == 1
                page.get_by_text("群名称", exact=True).wait_for()
                page.get_by_role("button", name="修改群名称", exact=True).click()
                page.get_by_role("textbox", name="新的群名称", exact=True).fill("新的合成群名")
                page.get_by_text("将“合成测试群”改为“新的合成群名”。", exact=True).wait_for()
                page.get_by_role("button", name="确认改名", exact=True).click()
                page.get_by_text("已提交，等待微信确认 · 操作 group-rename-1", exact=True).wait_for()
                assert len(group_actions) == 2
                page.get_by_role("button", name="刷新群资料", exact=True).click()
                page.get_by_text("新的合成群名（来自本机缓存）", exact=True).wait_for()
                page.get_by_text("已确认完成 · 操作 group-rename-1", exact=True).wait_for()
                assert len(group_actions) == 2
                page.get_by_text("我的群昵称", exact=True).wait_for()
                page.get_by_text("合成群昵称（来自本机缓存）", exact=True).wait_for()
                page.get_by_role("button", name="修改我的群昵称", exact=True).click()
                page.get_by_role("textbox", name="新的群昵称", exact=True).fill("")
                page.get_by_text("将“合成测试群”中的我的群昵称清空。", exact=True).wait_for()
                page.get_by_role("button", name="确认修改昵称", exact=True).click()
                page.get_by_text("已提交，等待微信确认 · 操作 group-my-name-1", exact=True).wait_for()
                assert len(group_actions) == 3
                page.get_by_role("button", name="刷新群资料", exact=True).click()
                page.get_by_text("未设置（来自本机缓存）", exact=True).wait_for()
                page.get_by_text("已确认完成 · 操作 group-my-name-1", exact=True).wait_for()
                assert len(group_actions) == 3
                page.locator(".group-directory").get_by_role("button", name="下一页", exact=True).click()
                page.get_by_role("button", name="未知状态群 · unknown-room@chatroom", exact=True).click()
                page.get_by_text("当前免打扰状态未取得，可刷新后再决定。", exact=True).wait_for()
                page.get_by_text("提交结果待核对 · 操作 group-mute-unknown", exact=True).wait_for()
                assert page.get_by_role("button", name="设为免打扰", exact=True).count() == 0
                page.get_by_text("当前连接不能修改这个群的免打扰；仍可刷新查看已有操作回执。", exact=True).wait_for()
                page.get_by_text("提交结果待核对 · 操作 group-rename-unknown", exact=True).wait_for()
                assert page.get_by_role("button", name="修改群名称", exact=True).count() == 0
                page.get_by_text("这次改名仍在核对中；刷新只查询原操作，不会重复提交。", exact=True).wait_for()
                page.get_by_text("提交结果待核对 · 操作 group-my-name-unknown", exact=True).wait_for()
                assert page.get_by_role("button", name="修改我的群昵称", exact=True).count() == 0
                page.get_by_text("这次群昵称修改仍在核对中；刷新只查询原操作，不会重复提交。", exact=True).wait_for()
                assert len(group_queries) == 2
                page.set_viewport_size({"width": 375, "height": 780})
                assert_no_horizontal_overflow(page, "WeChat group directory")
                assert_worker_controls_fit(page)
                page.screenshot(path=str(output / "apps-groups-narrow.png"), full_page=True)
                page.set_viewport_size({"width": 1280, "height": 800})
                page.get_by_text("群聊", exact=True).click()
                page.get_by_text("通讯录", exact=True).click()
                share = {"installed": False, "enabled": False, "runtime_ready": True, "revision": 0, "configuration": {"revision": 0, "values": {}, "configured_secrets": []}}
                def share_settings(route):
                    if route.request.method == "PUT":
                        assert route.request.headers.get("x-csrf-token")
                        body = route.request.post_data_json
                        if body["action"] == "install":
                            assert body["grant_network"] is True
                            share.update(installed=True, revision=1)
                        else:
                            assert body["revision"] == share["revision"]
                            if body["action"] == "configure":
                                config = body["configuration"]
                                assert config["secrets"] == {"yuanbao_cookie": "synthetic-browser-credential"}
                                share["configuration"] = {"revision": 1, "values": config["values"], "configured_secrets": ["yuanbao_cookie"]}
                            else:
                                share.update(enabled=body["action"] == "enable", revision=share["revision"] + 1)
                    fulfill_json(share)(route)
                page.route(status_pattern + "/share-reader", share_settings)
                page.get_by_text("视频号解析", exact=True).click()
                page.get_by_role("button", name="安装并授予受控联网权限").click()
                page.get_by_label("元宝解析凭据", exact=True).fill("synthetic-browser-credential")
                page.get_by_role("button", name="保存解析设置").click()
                page.get_by_placeholder("已配置，留空保留").wait_for()
                assert page.get_by_label("元宝解析凭据", exact=True).input_value() == ""
                page.set_viewport_size({"width": 375, "height": 780})
                assert_no_horizontal_overflow(page, "share reader configuration")
                assert_worker_controls_fit(page)
                page.screenshot(path=str(output / "apps-share-reader-narrow.png"), full_page=True)
                page.set_viewport_size({"width": 1280, "height": 800})
                page.get_by_role("button", name="启用解析", exact=True).click()
                page.get_by_text("已启用，按需运行", exact=True).wait_for()
                page.get_by_role("button", name="停用解析", exact=True).click()
                page.get_by_text("已停用", exact=True).wait_for()
                page.get_by_text("视频号解析", exact=True).click()
                media = {"installed": False, "enabled": False, "runtime_ready": True, "revision": 0}

                def media_settings(route):
                    if route.request.method == "PUT":
                        assert route.request.headers.get("x-csrf-token")
                        body = route.request.post_data_json
                        if body["action"] == "install":
                            assert body["grant_media"] is True
                            media.update(installed=True, revision=1)
                        elif body["action"] == "configure":
                            assert body["revision"] == media["revision"]
                            config = body["configuration"]
                            assert config["values"] == {"vision_endpoint": "https://vision.example/chat/completions", "vision_model": "synthetic-vision"}
                            assert config["secrets"] == {"vision_key": "synthetic-media-browser-key"}
                            media["configuration"] = {"revision": 1, "values": config["values"], "configured_secrets": ["vision_key"]}
                        else:
                            assert body["revision"] == media["revision"]
                            media.update(enabled=body["action"] == "enable", revision=media["revision"] + 1)
                    fulfill_json(media)(route)

                page.route(status_pattern + "/media-understanding", media_settings)
                page.get_by_text("媒体理解", exact=True).click()
                page.get_by_role("button", name="安装并授权媒体处理和模型调用").click()
                page.get_by_label("视觉接口地址", exact=True).fill("https://vision.example/chat/completions")
                page.get_by_label("视觉模型", exact=True).fill("synthetic-vision")
                page.get_by_label("视觉接口密钥", exact=True).fill("synthetic-media-browser-key")
                page.get_by_role("button", name="保存媒体设置").click()
                key_field = page.get_by_label("视觉接口密钥", exact=True).and_(page.get_by_placeholder("已配置，留空保留"))
                key_field.wait_for()
                assert key_field.input_value() == ""
                page.set_viewport_size({"width": 375, "height": 780})
                assert_no_horizontal_overflow(page, "media configuration")
                assert_worker_controls_fit(page)
                page.screenshot(path=str(output / "apps-media-understanding-narrow.png"), full_page=True)
                page.set_viewport_size({"width": 1280, "height": 800})
                page.get_by_role("button", name="启用媒体理解", exact=True).click()
                page.get_by_role("button", name="停用媒体理解", exact=True).click()
                page.get_by_role("button", name="启用媒体理解", exact=True).wait_for()
                page.get_by_text("媒体理解", exact=True).click()
                # Reading delivery history and receipt checks must never post a send.
                receipt_state = {"version": "first", "reads": 0, "checks": 0, "stops": 0, "resumes": 0, "continuation": None,
                                 "recall_queries": [], "recall_posts": []}
                receipts_pattern = re.compile(r"/api/apps/wechat/connections/synthetic-wechat/receipts(?:[/?].*)?$")
                pending_receipt = {"idempotency_key": "synthetic-send", "kind": "voice", "resumed_from": "synthetic-original",
                    "conversation_id": "synthetic-room@chatroom", "state": "submission_unknown",
                    "lookup": "stored", "operation_id": "synthetic-job", "accepted_parts": [{"index": 0, "server_id": "101"}, {"index": 1, "server_id": "102"}],
                    "unconfirmed_parts": [{"index": 2, "state": "submitting"}, {"index": 3, "state": "cancelled"}]}
                accepted_receipt = {"idempotency_key": "synthetic-direct", "kind": "file", "conversation_id": "synthetic-direct@chatroom",
                    "state": "accepted", "server_id": "201", "lookup": "platform"}
                def receipt_records(route):
                    parsed = urlparse(route.request.url)
                    if "/recall" in parsed.path:
                        params = parse_qs(parsed.query)
                        server_id = params.get("server_id", [""])[0]
                        cursor = params.get("cursor", [""])[0]
                        if route.request.method == "POST":
                            assert parsed.path.endswith("/receipts/synthetic-send/recall")
                            assert route.request.headers.get("x-csrf-token"), "recall lacked owner CSRF"
                            body = route.request.post_data_json
                            assert body == {"server_id": "102", "message_id": "msg_voice_part"}, "recall must target only the selected accepted part"
                            receipt_state["recall_posts"].append(body)
                            fulfill_json({"operation_id": "recall-102", "state": "submitted_pending", "action": "recall"}, status=202)(route)
                            return
                        assert route.request.method == "GET", "recall lookup must be read-only before confirmation"
                        receipt_state["recall_queries"].append((parsed.path, server_id, cursor))
                        if parsed.path.endswith("/receipts/synthetic-send/recall") and server_id == "102":
                            if cursor == "part-102-next":
                                fulfill_json({"message_id": "msg_voice_part"})(route)
                            elif any(post["server_id"] == "102" for post in receipt_state["recall_posts"]):
                                fulfill_json({"operation": {"operation_id": "recall-102", "state": "submitted_pending"}})(route)
                            else:
                                fulfill_json({"next_cursor": "part-102-next"})(route)
                            return
                        if parsed.path.endswith("/receipts/synthetic-direct/recall") and server_id == "201":
                            fulfill_json({"operation": {"operation_id": "recall-201", "state": "unknown"}})(route)
                            return
                        fulfill_json({})(route)
                        return
                    if route.request.url.endswith("/receipts/synthetic-send/cancel"):
                        assert route.request.method == "POST"
                        assert route.request.headers.get("x-csrf-token"), "stop lacked owner CSRF"
                        receipt_state["stops"] += 1
                        if receipt_state["continuation"]:
                            receipt_state["continuation"] = {"state": "cancelled", "message": "续办已停止"}
                        fulfill_json({"state": "cancellation_requested", "scope": "originating_turn"}, status=202)(route)
                        return
                    if route.request.url.endswith("/receipts/synthetic-send/resume"):
                        assert route.request.method == "POST"
                        assert route.request.headers.get("x-csrf-token"), "resume lacked owner CSRF"
                        assert route.request.post_data_json["request_key"]
                        receipt_state["resumes"] += 1
                        receipt_state["continuation"] = {"state": "requested", "message": "续办请求已保存"}
                        fulfill_json({"continuation": receipt_state["continuation"]}, status=202)(route)
                        return
                    assert route.request.method == "GET", "receipt view attempted a mutation"
                    if route.request.url.endswith("/receipts/synthetic-continuation"):
                        fulfill_json({"idempotency_key": "synthetic-continuation", "kind": "voice", "conversation_id": "synthetic-room@chatroom", "state": "accepted", "lookup": "platform", "resumed_from": "synthetic-send"})(route)
                        return
                    if "/receipts/synthetic-send" in route.request.url:
                        receipt_state["checks"] += 1
                        if receipt_state["continuation"] and receipt_state["continuation"]["state"] == "requested":
                            receipt_state["continuation"] = {"state": "tracking", "message": "已建立续发任务", "delivery_key": "synthetic-continuation"}
                        fulfill_json(dict(pending_receipt, state="cancelled", cancel_requested=True, lookup="platform", continuation=receipt_state["continuation"]))(route)
                    else:
                        receipt_state["reads"] += 1
                        if "after=synthetic-send" in route.request.url:
                            receipt_state["version"] = "changed"
                        fulfill_json({"items": [pending_receipt, accepted_receipt], "snapshot": receipt_state["version"],
                            "next_after": "synthetic-send" if receipt_state["version"] == "first" else "",
                            "coverage": "current_worker_checkpoint"})(route)
                page.route(receipts_pattern, receipt_records)
                page.get_by_text("投递记录", exact=True).click()
                page.get_by_role("button", name="查看投递记录", exact=True).click()
                page.get_by_text(re.compile("语音续发.*发送结果待核对")).wait_for()
                page.get_by_role("button", name="继续查看", exact=True).click()
                page.get_by_text("投递记录已更新，请重新查看。", exact=True).wait_for()
                page.get_by_role("button", name="重新查看", exact=True).click()
                # A delivery can be recalled only after its server receipt is located. For multipart sends,
                # choose exactly one accepted part, never submit a bulk recall.
                multipart_row = page.locator(".work-record").filter(has_text="synthetic-room@chatroom")
                part_picker = multipart_row.get_by_label("选择要撤回的已确认消息", exact=True)
                part_picker.select_option("102")
                multipart_row.get_by_role("button", name="定位可撤回消息", exact=True).click()
                page.get_by_role("button", name="继续查找", exact=True).wait_for()
                assert not receipt_state["recall_posts"], "read-only lookup must not recall a message"
                page.get_by_role("button", name="继续查找", exact=True).click()
                page.get_by_role("button", name="确认撤回", exact=True).wait_for()
                page.get_by_role("button", name="确认撤回", exact=True).click()
                page.get_by_role("button", name="核对撤回状态", exact=True).wait_for()
                assert receipt_state["recall_posts"] == [{"server_id": "102", "message_id": "msg_voice_part"}]
                # Pending and unknown operations remain read-only lookup states; neither may create a duplicate POST.
                page.get_by_role("button", name="核对撤回状态", exact=True).click()
                direct_row = page.locator(".work-record").filter(has_text="synthetic-direct@chatroom")
                direct_row.get_by_role("button", name="定位可撤回消息", exact=True).click()
                direct_row.get_by_role("button", name="核对撤回状态", exact=True).wait_for()
                assert len(receipt_state["recall_posts"]) == 1
                assert any(path.endswith("/receipts/synthetic-send/recall") and server == "102" for path, server, _ in receipt_state["recall_queries"])
                assert any(path.endswith("/receipts/synthetic-direct/recall") and server == "201" for path, server, _ in receipt_state["recall_queries"])
                multipart_row.get_by_role("button", name="停止本回合投递", exact=True).click()
                multipart_row.get_by_role("button", name="停止意图已保存", exact=True).wait_for()
                assert multipart_row.get_by_role("button", name="停止意图已保存", exact=True).is_disabled()
                multipart_row.get_by_role("button", name="核对回执", exact=True).click()
                page.get_by_text(re.compile("语音.*正在停止，等待回执.*已接受 2 段.*待核对 1 段.*已取消 1 段")).wait_for()
                assert receipt_state["reads"] == 3 and receipt_state["checks"] == 1 and receipt_state["stops"] == 1
                page.get_by_role("button", name="续发未发送片段", exact=True).click()
                page.get_by_role("button", name="续办请求已保存", exact=True).wait_for()
                assert page.get_by_role("button", name="续办请求已保存", exact=True).is_disabled()
                multipart_row.get_by_role("button", name="核对回执", exact=True).click()
                page.get_by_text(re.compile("语音.*续发结果：.*微信已接受")).wait_for()
                multipart_row.get_by_role("button", name="停止续办", exact=True).click()
                page.get_by_text(re.compile("语音.*续办已停止")).wait_for()
                assert receipt_state["resumes"] == 1 and receipt_state["stops"] == 2
                artifact_id = "work-" + "a" * 64
                delivery_id = "export-" + "b" * 64
                share_requests = []
                def register_artifact(route):
                    assert route.request.post_data_json["path"] == "作品/合成文档.pdf"
                    fulfill_json({"artifact_id": artifact_id, "filename": "合成文档.pdf", "status": "registered"})(route)
                def work_grants(route):
                    if route.request.method == "POST":
                        body = route.request.post_data_json
                        assert body["artifact_id"] == artifact_id and body["thread_id"] == "synthetic-room@chatroom"
                        assert body["requester_id"] == "synthetic-person" and body["enabled"] is True
                        share_requests.append(body)
                        fulfill_json({"delivery_id": delivery_id, "enabled": True})(route)
                    else:
                        fulfill_json({"items": [{"delivery_id": delivery_id, "thread_id": "synthetic-room@chatroom", "requester_id": "synthetic-person", "enabled": True}]})(route)
                artifact_pattern = "**/api/local/artifacts"
                grants_pattern = status_pattern + "/work-grants*"
                page.route(artifact_pattern, register_artifact)
                page.route(grants_pattern, work_grants)
                page.get_by_text("登记家目录中的作品", exact=True).click()
                page.get_by_label("成品文件", exact=True).fill("作品/合成文档.pdf")
                page.get_by_role("button", name="登记作品", exact=True).click()
                page.locator("#artifact-result").filter(has_text=artifact_id).wait_for()
                page.get_by_text("授权作品交付", exact=True).click()
                page.get_by_label("作品编号", exact=True).fill(artifact_id)
                page.get_by_label("接收会话", exact=True).fill("synthetic-room@chatroom")
                page.get_by_label("请求者", exact=True).fill("synthetic-person")
                page.get_by_role("button", name="允许交付", exact=True).click()
                page.locator("#apps-feedback").filter(has_text="作品交付已授权").wait_for()
                page.get_by_role("button", name="查看已有授权", exact=True).click()
                page.get_by_role("button", name="已列出全部", exact=True).wait_for()
                assert len(share_requests) == 1
                page.set_viewport_size({"width": 375, "height": 780})
                assert_no_horizontal_overflow(page, "worker controls")
                assert_worker_controls_fit(page)
                page.screenshot(path=str(output / "apps-worker-narrow.png"), full_page=True)
                page.set_viewport_size({"width": 1280, "height": 800})
                page.unroute(artifact_pattern, register_artifact)
                page.unroute(grants_pattern, work_grants)
                page.unroute(receipts_pattern, receipt_records)
                page.unroute(worker_policy_pattern, save_worker)
                page.unroute(worker_lifecycle_pattern, set_worker)
                page.unroute(contacts_pattern, contact_directory)

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

                artifact_id = "work-" + "c" * 64
                def synthetic_chat(route):
                    if route.request.method != "GET":
                        route.continue_()
                        return
                    route.fulfill(content_type="application/json", body=json.dumps({"items": [
                        {"id": "synthetic-user", "role": "user", "text": "把成品给我", "time": "2026-11-06T09:00:00+08:00"},
                        {"id": "synthetic-assistant", "role": "assistant", "text": "已经做好：" + artifact_id, "time": "2026-11-06T09:00:02+08:00"},
                    ], "has_more": False, "busy": False}, ensure_ascii=False))
                chat_pattern = re.compile(r".*/api/local/chat\?.*")
                page.route(chat_pattern, synthetic_chat)
                page.reload()
                page.locator("#chat-view").wait_for(state="visible")
                download = page.get_by_role("link", name="下载已登记成品", exact=True)
                assert artifact_id in page.locator("#chat-stream").inner_text(), "assistant text must remain readable"
                assert download.get_attribute("href") == "/api/local/artifacts/" + artifact_id + "/download"
                page.unroute(chat_pattern, synthetic_chat)
                assert not errors, errors
                browser.close()
                print(json.dumps({"status": "passed", "model_calls": 0, "wechat_calls": 0, "scenes": ["login", "initialize", "project tool install enable disable upgrade", "artifact download link", "application catalog", "application source install or unavailable source", "synthetic application account", "synthetic QR retry and expiry", "legacy import preview confirm and stale retry", "legacy memory inventory and pagination", "worker policy and lifecycle", "WeChat contacts and independent grant", "WeChat group management receipts", "share reader settings", "media understanding settings", "delivery snapshot pagination", "partial cancellation receipt lookup", "artifact registration and sharing", "375 worker editor", "application account unavailable", "pause", "stale revision", "resume", "logout", "restart", "375 dark"]}, ensure_ascii=False))
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
