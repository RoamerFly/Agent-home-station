"""New memory-only archive form; synthetic responses, no native login or sends."""
import argparse
from pathlib import Path
import re
import tempfile
from playwright.sync_api import sync_playwright, expect
from test_local_home_ui import start, stop, home_tab, open_configuration


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-memory-archive-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "work")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1440, "height": 900})
                errors, calls = [], []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", lambda route: route.fulfill(json={"items": [{"id": "wechat", "name": "微信", "registration": "station-native"}]}))
                page.route("**/api/apps/wechat/connections", lambda route: route.fulfill(json={"items": [{"id": "wechat-synthetic-one", "label": "合成账号一"}, {"id": "wechat-synthetic-two", "label": "合成账号二"}]}))
                def archive(route):
                    body = route.request.post_data_json
                    calls.append(body)
                    assert route.request.headers.get("x-csrf-token")
                    assert body["source_database"] in ("C:/synthetic/memory.sqlite", "C:/synthetic/multi.sqlite")
                    if body["source_database"].endswith("multi.sqlite") and not body["source_account"]:
                        route.fulfill(json={"status": "choose_account", "source_sha256": "a" * 64, "source_accounts": ["old-synthetic", "second-synthetic"], "conversations": [], "applied": False})
                        return
                    assert body["source_account"] in ("", "old-synthetic", "second-synthetic")
                    selected = body["thread_id"]
                    assert not body["digest"] or body["same_account"]
                    route.fulfill(json={"status": "applied" if body["digest"] else "ready" if selected else "preview", "digest": "b" * 64 if selected else "", "source_account": body["source_account"] or "old-synthetic", "source_sha256": "a" * 64, "applied": bool(body["digest"]), "conversations": [{"thread_id": "synthetic-room", "event_count": 23, "memory_count": 2, "history_granted": True}]})
                page.route(re.compile(r"/api/apps/wechat/connections/[^/]+/memory-archive$"), archive)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                home_tab(page, "apps")
                open_configuration(page, "微信", "会话与记忆")
                panel = page.locator(".configuration-memory-import")
                assert not calls
                panel.get_by_label("旧记忆库完整路径", exact=True).fill("C:/synthetic/memory.sqlite")
                expect(panel.locator("input:not([type=checkbox])")).to_have_count(1)
                expect(panel.get_by_role("checkbox")).not_to_be_visible()
                page.locator("#app-configuration-connection").select_option("wechat-synthetic-two")
                expect(panel.get_by_label("旧记忆库完整路径", exact=True)).to_have_value("")
                page.locator("#app-configuration-connection").select_option("wechat-synthetic-one")
                expect(panel.get_by_label("旧记忆库完整路径", exact=True)).to_have_value("C:/synthetic/memory.sqlite")
                panel.get_by_role("button", name="查找旧会话", exact=True).click()
                panel.get_by_role("button", name="核对此会话", exact=True).click()
                expect(panel.get_by_role("checkbox")).to_be_visible()
                panel.get_by_role("button", name="确认接续此会话", exact=True).click()
                expect(panel.get_by_text("请先确认新旧连接属于同一微信账号。", exact=True)).to_be_visible()
                assert len(calls) == 2
                panel.get_by_role("checkbox").check()
                panel.get_by_role("button", name="确认接续此会话", exact=True).click()
                expect(panel.get_by_text("会话档案已接续。", exact=True)).to_be_visible()
                assert len(calls) == 3 and calls[-1]["digest"] == "b" * 64
                page.screenshot(path=str(output / "memory-archive-simple-desktop.png"))
                panel.get_by_label("旧记忆库完整路径", exact=True).fill("C:/synthetic/multi.sqlite")
                expect(panel.get_by_role("checkbox")).not_to_be_visible()
                expect(panel.get_by_role("button", name="确认接续此会话", exact=True)).to_have_count(0)
                page.keyboard.press("Escape")
                page.locator("#app-configuration-open").click()
                page.locator("#app-configuration-nav").get_by_role("button", name="会话与记忆", exact=True).click()
                expect(panel.get_by_label("旧记忆库完整路径", exact=True)).to_have_value("C:/synthetic/multi.sqlite")
                panel.get_by_role("button", name="查找旧会话", exact=True).click()
                expect(panel.locator(".configuration-archive-account")).to_have_count(2)
                expect(panel.locator("select")).to_have_count(0)
                page.screenshot(path=str(output / "memory-archive-account-cards.png"))
                page.emulate_media(reduced_motion="reduce")
                panel.get_by_role("button", name="旧资料账号 2", exact=False).click()
                expect(panel.get_by_role("button", name="核对此会话", exact=True)).to_be_visible()
                assert calls[-1]["source_account"] == "second-synthetic"
                assert panel.locator(".configuration-archive-card").evaluate("node => getComputedStyle(node).animationName") == "none"
                panel.get_by_label("旧记忆库完整路径", exact=True).fill("C:/synthetic/memory.sqlite")
                expect(panel.locator(".configuration-archive-proof")).to_have_count(0)
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
    print('{"status":"passed","scope":"new memory archive form","real_sends":0}')


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
