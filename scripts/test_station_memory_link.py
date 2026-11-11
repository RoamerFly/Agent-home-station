"""Focused synthetic UI replay for explicit cross-source chat continuation."""
import argparse
import json
from pathlib import Path
import tempfile

from playwright.sync_api import sync_playwright, expect
from test_local_home_ui import start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-memory-link-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "work")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1280, "height": 740})
                errors, sends = [], []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/local/model", lambda route: route.fulfill(json={"revision": 0, "provider": "deepseek", "model": "synthetic", "key_configured": True}))
                items = [{"id": "parent-u", "role": "user", "text": "按需查询合成资料", "request_id": "synthetic-parent", "time": "2026-11-11T01:00:00Z"},
                         {"id": "parent-a", "role": "assistant", "text": "合成来源结果", "request_id": "synthetic-parent", "time": "2026-11-11T01:00:01Z"}]
                failed = False

                def chat(route):
                    nonlocal failed
                    if route.request.method == "GET":
                        route.fulfill(json={"items": items, "memory_link_requests": ["synthetic-parent"], "busy": False, "has_more": False})
                        return
                    body = route.request.post_data_json
                    assert route.request.headers.get("x-csrf-token")
                    sends.append(body)
                    request = body["request_id"]
                    if not any(row["request_id"] == request for row in items):
                        items.append({"id": request + "-u", "role": "user", "text": body["text"], "request_id": request, "time": f"2026-11-11T01:00:{len(items) + 10:02}Z"})
                    if body["text"] == "重试仍保留原关联" and not failed:
                        failed = True
                        route.fulfill(status=503, json={"error": "合成临时模型失败"})
                        return
                    items.append({"id": request + "-a", "role": "assistant", "text": "合成回复", "request_id": request, "time": f"2026-11-11T01:00:{len(items) + 10:02}Z"})
                    route.fulfill(json={"request_id": request, "status": "completed", "messages": ["合成回复"]})

                page.route("**/api/local/chat*", chat)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                link = page.get_by_role("button", name="关联继续", exact=False)
                expect(link).to_have_count(1)
                link.click()
                expect(page.locator("#chat-memory-link")).to_be_visible()
                expect(page.locator("#chat-input")).to_be_focused()
                page.locator("#chat-input").fill("未发送的草稿")
                page.get_by_role("button", name="取消关联", exact=True).click()
                expect(page.locator("#chat-memory-link")).to_be_hidden()
                expect(page.locator("#chat-input")).to_have_value("未发送的草稿")
                assert not sends
                link.click()
                page.locator("#chat-input").fill("继续这一条")
                page.locator("#chat-send").click()
                expect(page.locator("#chat-memory-link")).to_be_hidden()
                expect(page.get_by_text("合成回复", exact=True)).to_have_count(1)
                assert sends[-1]["continue_request_id"] == "synthetic-parent"
                page.locator("#chat-input").fill("普通新回合")
                page.locator("#chat-send").click()
                expect(page.get_by_text("合成回复", exact=True)).to_have_count(2)
                assert "continue_request_id" not in sends[-1]
                link.click()
                page.locator("#chat-input").fill("重试仍保留原关联")
                page.locator("#chat-send").click()
                retry = page.locator(".bubble-failure").get_by_role("button", name="重试", exact=True)
                expect(retry).to_be_visible()
                first = dict(sends[-1])
                # Choosing another UI state cannot rebind the failed request.
                link.click()
                retry.click()
                expect(page.get_by_text("合成回复", exact=True)).to_have_count(3)
                assert sends[-1] == first and first["continue_request_id"] == "synthetic-parent"
                expect(page.locator("#chat-memory-link")).to_be_visible()
                assert page.locator("#chat-form").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
                page.screenshot(path=str(output / "memory-link-desktop-small.png"))
                page.get_by_role("button", name="取消关联", exact=True).click()
                page.set_viewport_size({"width": 1440, "height": 900})
                expect(page.locator("#chat-memory-link")).to_be_hidden()
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
    print(json.dumps({"status": "passed", "scope": "new chat memory link only", "real_model_or_platform_actions": 0}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
