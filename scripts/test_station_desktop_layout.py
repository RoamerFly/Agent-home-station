"""Desktop layout acceptance only; synthetic offline app, no model or platform actions."""
import argparse
import json
from pathlib import Path
import tempfile

from playwright.sync_api import sync_playwright, expect
from test_local_home_ui import start, stop, home_tab, open_configuration, close_application


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-desktop-layout-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "work")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                page = browser.new_page(viewport={"width": 1440, "height": 900})
                errors = []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.route("**/api/apps", lambda route: route.fulfill(json={"items": [{"id": "synthetic-desktop", "name": "合成桌面应用", "registration": "synthetic"}]}))
                page.route("**/api/apps/synthetic-desktop/connections", lambda route: route.fulfill(json={"items": []}))
                page.route("**/api/local/apps/synthetic-desktop/configuration", lambda route: route.fulfill(json={"id": "synthetic-desktop", "registered": True, "sections": [{"id": "overview", "title": "概览"}, {"id": "account", "title": "账号与授权"}, {"id": "source", "title": "资料与版本"}]}))
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                sizes = [(1024, 600), (1280, 800), (1440, 900)]
                for width, height in sizes:
                    close_application(page)
                    page.set_viewport_size({"width": width, "height": height})
                    home_tab(page, "chat")
                    rail = page.locator(".home-visual").bounding_box()
                    content = page.locator(".home-content").bounding_box()
                    assert rail["x"] + rail["width"] <= content["x"] + 1, (width, rail, content)
                    expect(page.locator("#chat-tab")).to_be_visible()
                    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
                    home_tab(page, "memory")
                    expect(page.locator("#memory-view")).to_be_visible()
                    canvas = page.locator(".memory-canvas").bounding_box()
                    detail = page.locator(".memory-detail").bounding_box()
                    assert canvas["x"] + canvas["width"] <= detail["x"] + 1, (width, canvas, detail)
                    home_tab(page, "apps")
                    page.locator('#apps-list[aria-busy="false"]').wait_for()
                    open_configuration(page, "合成桌面应用", "概览")
                    modal = page.locator("#app-configuration")
                    nav = page.locator("#app-configuration-nav")
                    body = page.locator("#app-configuration-body")
                    bounds, left, right = modal.bounding_box(), nav.bounding_box(), body.bounding_box()
                    assert 0 <= bounds["x"] and bounds["x"] + bounds["width"] <= width + 1
                    assert 0 <= bounds["y"] and bounds["y"] + bounds["height"] <= height + 1
                    assert left["x"] + left["width"] <= right["x"] + 1
                    assert body.evaluate("node => node.scrollWidth <= node.clientWidth + 1")
                    # Long configuration content scrolls without moving its header.
                    top = modal.locator(".app-dialog-header").bounding_box()["y"]
                    body.evaluate("node => { const filler = document.createElement('div'); filler.style.height = '1800px'; node.append(filler); node.scrollTop = node.scrollHeight; }")
                    assert body.evaluate("node => node.scrollTop > 0")
                    assert modal.locator(".app-dialog-header").bounding_box()["y"] == top
                    page.screenshot(path=str(output / f"desktop-configuration-{width}.png"))
                    page.keyboard.press("Escape")
                    expect(modal).not_to_be_visible()
                    expect(page.locator("#app-configuration-open")).to_be_focused()
                assert not errors, errors
                browser.close()
        finally:
            stop(process)
    print(json.dumps({"status": "passed", "scope": "desktop layout only", "viewports": sizes, "real_model_or_platform_actions": 0}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
