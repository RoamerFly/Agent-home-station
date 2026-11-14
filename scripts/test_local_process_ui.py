"""Synthetic desktop replay for readable execution progress and task results."""

import argparse
import json
import tempfile
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import expect, sync_playwright
from test_local_home_ui import (
    app_connection_payload,
    fulfill_json,
    open_configuration,
    start,
    stop,
    wechat_connection_payload,
)


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="simple-process-", dir=output) as temporary:
        root = Path(temporary).resolve()
        process, origin = start(binary, root / "data", root / "workspace")
        calls = []
        errors = []
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1440, "height": 900})
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", fulfill_json(app_connection_payload()))
                page.route("**/api/apps/wechat/connections", fulfill_json(wechat_connection_payload()))
                page.route("**/api/local/apps/wechat/configuration", fulfill_json({
                    "id": "wechat", "name": "微信", "registered": True,
                    "sections": [
                        {"id": "overview", "title": "概览"},
                        {"id": "rounds", "title": "执行回合"},
                        {"id": "tasks", "title": "任务与文档"},
                    ],
                }))

                def rounds(route):
                    query = parse_qs(urlparse(route.request.url).query)
                    calls.append({"rounds": query})
                    events = [
                        (1, "message_received", "received"),
                        (2, "generation_started", "decision"),
                        (3, "message_sent", "sent"),
                    ]
                    steps = [{
                        "sequence": sequence, "event": event, "category": category,
                        "at": f"2026-11-12T02:00:0{sequence}Z",
                        "thread": "private-thread-id", "message": "private-message-id",
                        "receipt": "private-receipt-id", "status": "private-status-code",
                        "tool": "memory_search" if sequence == 2 else "",
                    } for sequence, event, category in reversed(events)]
                    fulfill_json({"date": "2026-11-12", "steps": steps,
                                  "messages": [{"thread": "private-thread-id", "message": "private-message-id", "text": "帮我整理一下这份文档"}],
                                  "next_before": "", "truncated": False})(route)

                page.route("**/execution-rounds?*", rounds)
                row = {"id": "media-file", "kind": "file", "state": "private-status-code",
                       "thread": "private-thread-id", "message": "private-message-id",
                       "receipt": "private-receipt-id"}
                page.route("**/connections/*/tasks?*", fulfill_json({"items": [row], "next_after": ""}))
                page.route("**/tasks/media-file", fulfill_json({
                    "task": row, "documents": [{"thread_id": row["thread"], "message_id": row["message"]}],
                }))
                page.route("**/tasks/media-file/attachment", fulfill_json({
                    "filename": "示例文档.docx", "bytes": 128, "sha256": "private-file-digest",
                    "asset_id": "wa-" + "a" * 64,
                }))
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                page.locator("#apps-tab").click()
                page.locator("#apps-list[aria-busy='false']").wait_for()
                open_configuration(page, "微信", "概览")
                assert not calls, "opening an application eagerly read execution history"
                open_configuration(page, "微信", "执行回合")
                body = page.locator("#app-configuration-body")
                card = body.locator(".round-message-card")
                card.get_by_text("帮我整理一下这份文档", exact=True).wait_for()
                expect(card.locator(".round-timeline li")).to_have_count(3)
                expect(card.locator(".round-tool-name")).to_contain_text("memory_search")
                assert " 次工具调用" not in card.inner_text()
                assert body.locator("input").count() == 1
                assert body.locator("input").get_attribute("type") == "date"
                assert "private-" not in body.inner_text()
                page.screenshot(path=str(output / "round-cards.png"), full_page=True)
                opener = card.get_by_role("button", name="单独查看", exact=True)
                opener.click()
                dialog = page.get_by_role("dialog", name="回合处理过程", exact=True)
                expect(dialog.locator(".round-timeline li")).to_have_count(3)
                expect(dialog.locator(".round-tool-name")).to_contain_text("memory_search")
                for label in ("收到消息", "正在理解需求", "微信已接受回复"):
                    expect(dialog).to_contain_text(label)
                assert "private-" not in dialog.inner_text()
                page.screenshot(path=str(output / "readable-rounds.png"), full_page=True, animations="disabled")
                dialog.get_by_role("button", name="关闭", exact=True).click()
                expect(opener).to_be_focused()
                open_configuration(page, "微信", "任务与文档")
                body.get_by_text("文件任务 · 状态待确认", exact=True).click()
                body.get_by_role("button", name="查看文件", exact=True).click()
                body.get_by_text("示例文档.docx", exact=True).wait_for()
                body.get_by_role("link", name="保存原件", exact=True).wait_for()
                assert body.locator("input").count() == 0
                assert "private-" not in body.inner_text()
                page.screenshot(path=str(output / "simple-task-result.png"), full_page=True)
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
        (output / "result.json").write_text(json.dumps({
            "process_preserved": True, "round_cards_and_dialog": True, "tool_calls_visible": True,
            "technical_fields_hidden": True, "reads": calls, "browser_errors": errors,
        }, ensure_ascii=False, indent=2), encoding="utf8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    arguments = parser.parse_args()
    verify(arguments.binary.resolve(), arguments.output.resolve())
