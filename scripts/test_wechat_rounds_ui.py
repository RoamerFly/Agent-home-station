"""Synthetic Windows desktop replay of complete WeChat execution-round cards.

Uses an isolated local Station and intercepted account/round APIs. No native
WeChat, model or external platform call is made. Assets come from the checkout
so this can also inspect a changed UI before rebuilding its embedded binary.
"""

import argparse
import json
import tempfile
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import expect, sync_playwright

from test_local_home_ui import (
    app_connection_payload, fulfill_json, home_tab, open_configuration, start, stop,
)


DATE = "2026-11-14"
THREAD = "synthetic-round-thread"
MESSAGE = "synthetic-round-message"
REQUEST = "请逐项查阅这份合成资料，然后生成语音。\n" + "这是一段应完整显示而不能截断的合成请求。" * 35 + "\n末尾标记：全部消息可见 <img src=invalid onerror=alert(1)>"


def step(sequence, event, **fields):
    return {"sequence": sequence, "at": f"{DATE}T10:{sequence // 60:02}:{sequence % 60:02}Z",
            "thread": THREAD, "message": MESSAGE, "event": event,
            "category": "tool" if event.startswith("tool_") else "decision", **fields}


def fixture_steps():
    steps = [step(1, "message_received", category="received", status="received", tool_tracking_version=1),
             step(2, "generation_started", status="processing")]
    for call in range(1, 63):
        # Identical tool names and completion labels must remain distinct rows.
        tool = "memory_search" if call % 2 else "synthetic_custom_tool"
        values = {"tool": tool, "call_id": f"synthetic-call-{call}", "tool_attempt": 1}
        steps.append(step(len(steps) + 1, "tool_started", status="running", **values))
        steps.append(step(len(steps) + 1, "tool_completed", status="succeeded",
                          duration_ms=1250 + call, **values))
    values = {"tool": "project_audio_cpp_synthesize", "call_id": "synthetic-call-retry"}
    steps.extend([
        step(127, "tool_failed", category="error", status="failed", tool_attempt=1,
             tool_code="service_unavailable", tool_stage="request", duration_ms=1700, **values),
        step(128, "tool_retry_scheduled", status="retrying", tool_attempt=2,
             tool_retries=1, **values),
        step(129, "tool_started", status="running", tool_attempt=2, **values),
        step(130, "tool_completed", status="succeeded", tool_attempt=2, duration_ms=3200, **values),
        step(131, "utterance_plan_queued", status="queued"),
        step(132, "message_sent", category="sent", status="accepted"),
        step(133, "message_received", thread="synthetic-media-thread", message="synthetic-media-message",
             category="received", status="received"),
        step(134, "media_understood", thread="synthetic-media-thread", message="synthetic-media-message",
             status="succeeded"),
    ])
    return steps


def verify(binary, output, embedded_assets=False):
    output.mkdir(parents=True, exist_ok=True)
    assets = Path(__file__).resolve().parents[1] / "internal/stationui/static"
    results = {"real_wechat_sends": 0, "real_model_calls": 0, "external_requests": [],
               "unexpected_platform_mutations": [],
               "assets": "embedded_binary" if embedded_assets else "checkout_override"}
    with tempfile.TemporaryDirectory(prefix="synthetic-rounds-", dir=output) as temporary:
        root = Path(temporary).resolve()
        process, origin = start(binary, root / "data", root / "workspace")
        reads, errors, pending = [], [], []
        revoked = False
        hold_details = False
        fail_details_once = True
        fail_older_once = False
        presentation_demo = False
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1440, "height": 1000}, reduced_motion="reduce")
                page.on("pageerror", lambda error: errors.append(str(error)))

                def restrict_network(route):
                    if not route.request.url.startswith(origin + "/"):
                        results["external_requests"].append(route.request.url)
                        route.abort()
                    elif (route.request.method != "GET" and
                          urlparse(route.request.url).path.startswith(("/api/apps/", "/api/local/apps/"))):
                        results["unexpected_platform_mutations"].append({"method": route.request.method,
                                                                       "path": urlparse(route.request.url).path})
                        fulfill_json({"error": "合成测试禁止真实应用写入"}, 403)(route)
                    else:
                        route.continue_()

                page.route("**/*", restrict_network)
                def serve_asset(file, mime):
                    return lambda route: route.fulfill(body=(assets / file).read_bytes(), content_type=mime)
                if not embedded_assets:
                    for name, content_type in (("local-home.js", "application/javascript"), ("local-home.css", "text/css")):
                        page.route(f"**/assets/{name}", serve_asset(name, content_type))
                page.route("**/api/local/apps/catalog", fulfill_json({"applications": [
                    {"id": "wechat", "name": "微信", "install_state": "installed"}]}))
                page.route("**/api/apps", fulfill_json(app_connection_payload()))
                page.route("**/api/apps/wechat/connections", fulfill_json({"items": [
                    {"id": "synthetic-one", "label": "合成账号一", "primary": True},
                    {"id": "synthetic-two", "label": "合成账号二"},
                ]}))
                account = {"login": "authenticated", "runtime": "online", "worker": "paused",
                           "configured_policy": {"conversation_enabled": True}}
                for connection in ("synthetic-one", "synthetic-two"):
                    page.route(f"**/api/apps/wechat/connections/{connection}", fulfill_json({
                        "connection": {**account, "id": connection}}))
                page.route("**/api/local/apps/wechat/configuration", fulfill_json({
                    "id": "wechat", "registered": True, "sections": [
                        {"id": "overview", "title": "概览"}, {"id": "rounds", "title": "执行回合"}]}))

                def round_response(query, connection):
                    if presentation_demo:
                        items = [step(1, "message_received", category="received", status="received", tool_tracking_version=1),
                                 step(2, "generation_started", status="processing"),
                                 step(3, "tool_started", status="running", tool="memory_search", call_id="demo-memory", tool_attempt=1),
                                 step(4, "tool_completed", status="succeeded", tool="memory_search", call_id="demo-memory", tool_attempt=1, duration_ms=420),
                                 step(5, "tool_started", status="running", tool="project_audio_cpp_synthesize", call_id="demo-speech", tool_attempt=1),
                                 step(6, "tool_completed", status="succeeded", tool="project_audio_cpp_synthesize", call_id="demo-speech", tool_attempt=1, duration_ms=2300),
                                 step(7, "message_sent", category="sent", status="accepted")]
                        messages = [{"thread": THREAD, "message": MESSAGE, "state": "available", "kind": "text",
                                     "text": "查一下我们上次聊的露营计划，再帮我生成一段出发提醒语音。", "sender": "合成测试用户"}]
                    elif connection == "synthetic-two":
                        items = [step(1, "message_received", thread="other-thread", message="other-message",
                                      category="received", status="received")]
                        messages = [{"thread": "other-thread", "message": "other-message", "text": "这是账号二的独立合成消息",
                                     "state": "available", "kind": "text", "sender": "合成人物二"}]
                    else:
                        items = fixture_steps()
                        messages = [{"thread": THREAD, "message": MESSAGE,
                                     "state": "changed_or_forgotten" if revoked else "available", "kind": "text",
                                     **({} if revoked else {"text": REQUEST, "sender": "合成人物一"})},
                                    {"thread": "synthetic-media-thread", "message": "synthetic-media-message",
                                     "state": "available", "text": "[收到语音消息]", "kind": "voice", "sender": "合成人物一"}]
                    if query.get("thread"):
                        items = [item for item in items if item["thread"] == query["thread"][0]
                                 and item["message"] == query["message"][0]]
                    if query.get("before"):
                        items = [item for item in items if item["sequence"] < int(query["before"][0])]
                    items = sorted(items, key=lambda item: item["sequence"], reverse=True)
                    limit = int(query.get("limit", ["40"])[0])
                    selected = items[:limit]
                    keys = {(item["thread"], item["message"]) for item in selected}
                    messages = [item for item in messages if (item["thread"], item["message"]) in keys]
                    return {"date": DATE, "steps": selected, "messages": messages, "effective_limit": limit,
                            "next_before": str(selected[-1]["sequence"]) if len(items) > len(selected) else "",
                            "truncated": False, "coverage": "station_runtime"}

                def rounds(route):
                    nonlocal fail_details_once, fail_older_once
                    parsed = urlparse(route.request.url)
                    query = parse_qs(parsed.query)
                    connection = parsed.path.split("/")[-2]
                    reads.append({"connection": connection, "query": query})
                    if fail_older_once and query.get("before") and not query.get("message"):
                        fail_older_once = False
                        fulfill_json({"error": "合成较早记录读取失败"}, 503)(route)
                        return
                    if query.get("message") == [MESSAGE] and connection == "synthetic-one":
                        if hold_details:
                            pending.append((route, round_response(query, connection)))
                            return
                        if fail_details_once:
                            fail_details_once = False
                            fulfill_json({"error": "合成读取失败，请重试"}, 503)(route)
                            return
                    fulfill_json(round_response(query, connection))(route)

                page.route("**/execution-rounds?*", rounds)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                home_tab(page, "apps")
                page.locator("#apps-list[aria-busy='false']").wait_for()
                open_configuration(page, "微信", "概览")
                assert not reads, "overview eagerly loaded execution history"
                open_configuration(page, "微信", "执行回合")
                body = page.locator("#app-configuration-body")
                expect(body.locator("article.round-message-card")).to_have_count(2)
                card = body.locator("article.round-message-card").nth(1)
                retry = card.get_by_role("button", name="重试读取本轮过程", exact=True)
                expect(retry).to_be_visible()
                assert card.locator(".round-timeline li").count() > 1, "failed detail hid existing steps"
                retry.click()
                expect(card.locator(".round-card-feedback")).to_have_text("本轮处理记录已完整载入。")
                expect(card.locator(".round-card-preview")).to_have_text(REQUEST)
                expect(card.locator("img")).to_have_count(0)
                expect(card.locator(".round-timeline li")).to_have_count(132)
                expect(card.locator(".round-tool-step")).to_have_count(128)
                assert card.locator(".round-timeline li").evaluate_all(
                    "nodes => nodes.map(node => Number(node.dataset.sequence))") == list(range(1, 133))
                expect(card.locator('.round-tool-step[data-call-id="synthetic-call-1"]')).to_have_count(2)
                expect(card.locator('.round-tool-step[data-call-id="synthetic-call-retry"]')).to_have_count(4)
                expect(card.locator(".round-history-note")).to_have_text("")
                assert len([r for r in reads if r["query"].get("message") == [MESSAGE]]) >= 3
                assert any(r["query"].get("before") for r in reads if r["query"].get("message") == [MESSAGE])
                assert len(card.locator(".round-tool-name").filter(has_text="memory_search").all_text_contents()) == 62
                assert len(card.locator(".round-tool-name").filter(has_text="synthetic_custom_tool").all_text_contents()) == 62
                assert " 个步骤" not in card.inner_text() and " 次工具调用" not in card.inner_text()
                assert "正在理解需求" in card.inner_text() and "微信已接受回复" in card.inner_text()
                assert "重试" in card.inner_text() and "秒" in card.inner_text()
                assert "service_unavailable" not in card.inner_text(), "diagnostic code replaced readable tool status"
                media = body.locator("article.round-message-card").filter(has_text="[收到语音消息]")
                expect(media).to_have_count(1)

                # Existing list pagination must not duplicate an already completed card.
                earlier = body.get_by_role("button", name="较早记录", exact=True)
                if earlier.is_visible():
                    earlier.click()
                    expect(card.locator(".round-timeline li")).to_have_count(132)
                opener = card.get_by_role("button", name="单独查看", exact=True)
                opener.click()
                dialog = page.get_by_role("dialog", name="回合处理过程", exact=True)
                expect(dialog.locator(".round-timeline li")).to_have_count(132)
                dialog.get_by_role("button", name="关闭", exact=True).click()
                expect(opener).to_be_focused()

                for width, height in ((1440, 1000), (1280, 800), (1024, 720)):
                    page.set_viewport_size({"width": width, "height": height})
                    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1")
                    assert body.evaluate("node => node.scrollWidth <= node.clientWidth + 1"), width
                    assert card.evaluate("node => node.scrollWidth <= node.clientWidth + 1"), width
                    body.evaluate("node => node.scrollTop = 0")
                    page.screenshot(path=str(output / f"round-cards-{width}.png"), animations="disabled")
                page.set_viewport_size({"width": 1440, "height": 1000})
                card.locator(".round-timeline li").nth(2).scroll_into_view_if_needed()
                page.screenshot(path=str(output / "round-tool-starts.png"), animations="disabled")
                card.locator(".round-timeline li").nth(126).scroll_into_view_if_needed()
                page.screenshot(path=str(output / "round-tool-retry.png"), animations="disabled")

                # A refreshed permission projection must discard the old source text.
                revoked = True
                page.locator("#app-configuration-refresh").click()
                expect(body.locator("article.round-message-card")).to_have_count(2)
                expect(card.locator(".round-card-feedback")).to_have_text("本轮处理记录已完整载入。")
                expect(body).not_to_contain_text("末尾标记：全部消息可见")
                revoked = False

                # Loading the previous list page must not strand requests from
                # the earlier generation in a permanent per-card busy state.
                hold_details = True
                open_configuration(page, "微信", "概览")
                open_configuration(page, "微信", "执行回合")
                for _ in range(30):
                    if pending:
                        break
                    page.wait_for_timeout(20)
                assert pending, "pagination-race fixture did not delay a detail read"
                body.get_by_role("button", name="较早记录", exact=True).click()
                for _ in range(30):
                    if len(pending) >= 2:
                        break
                    page.wait_for_timeout(20)
                assert len(pending) >= 2, "list pagination did not restart the unfinished detail read"
                hold_details = False
                for route, response in pending:
                    route.fulfill(json=response)
                pending.clear()
                expect(body.locator("article.round-message-card[aria-busy='true']")).to_have_count(0)
                expect(card.locator(".round-timeline li")).to_have_count(132)
                expect(card.locator(".round-card-feedback")).to_have_text("本轮处理记录已完整载入。")

                # A list-page failure that invalidated an old detail read must
                # leave a visible retry action, rather than an idle dead end.
                hold_details = True
                open_configuration(page, "微信", "概览")
                open_configuration(page, "微信", "执行回合")
                for _ in range(30):
                    if pending:
                        break
                    page.wait_for_timeout(20)
                assert pending, "list-failure fixture did not delay a detail read"
                fail_older_once = True
                body.get_by_role("button", name="较早记录", exact=True).click()
                retry = card.get_by_role("button", name="重试读取本轮过程", exact=True)
                expect(retry).to_be_visible()
                hold_details = False
                for route, response in pending:
                    route.fulfill(json=response)
                pending.clear()
                retry.click()
                expect(card.locator(".round-card-feedback")).to_have_text("本轮处理记录已完整载入。")
                expect(card.locator(".round-timeline li")).to_have_count(132)

                # Delay the old account's detail request, switch accounts, then release it.
                hold_details = True
                open_configuration(page, "微信", "概览")
                open_configuration(page, "微信", "执行回合")
                body.locator(".round-card-preview").first.wait_for()
                for _ in range(30):
                    if pending:
                        break
                    page.wait_for_timeout(20)
                assert pending, "account-isolation fixture did not intercept a detail read"
                page.locator("#app-configuration-connection").select_option("synthetic-two")
                expect(body).to_contain_text("这是账号二的独立合成消息")
                for route, response in pending:
                    route.fulfill(json=response)
                page.wait_for_timeout(100)
                expect(body).not_to_contain_text("末尾标记：全部消息可见")
                expect(body.locator("article.round-message-card")).to_have_count(1)
                presentation_demo = True
                hold_details = False
                page.locator("#app-configuration-connection").select_option("synthetic-one")
                demo = body.locator("article.round-message-card")
                expect(demo.locator(".round-card-feedback")).to_have_text("本轮处理记录已完整载入。")
                expect(demo.locator(".round-timeline li")).to_have_count(7)
                page.set_viewport_size({"width": 1440, "height": 1100})
                body.evaluate("node => node.scrollTop = 0")
                page.screenshot(path=str(output / "round-card-short-message-tools.png"), animations="disabled")
                assert not errors, errors
                assert not results["external_requests"], results["external_requests"]
                assert not results["unexpected_platform_mutations"], results["unexpected_platform_mutations"]
                results.update({"status": "passed", "long_message_full": True, "explicit_tool_events": 128,
                                "complete_round_steps": 132, "pagination_over_100": True,
                                "retry_preserves_loaded_steps": True, "source_revocation_clears_preview": True,
                                "list_pagination_during_detail_load_completes": True,
                                "list_page_failure_can_retry_interrupted_round": True,
                                "late_response_cannot_cross_accounts": True, "desktop_widths": [1440, 1280, 1024],
                                "browser_errors": errors, "reads": reads})
                browser.close()
        finally:
            stop(process)
    (output / "result.json").write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding="utf8")
    print(json.dumps({key: value for key, value in results.items() if key != "reads"}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--embedded-assets", action="store_true", help="verify the rebuilt executable's embedded UI instead of overriding checkout assets")
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve(), args.embedded_assets)
