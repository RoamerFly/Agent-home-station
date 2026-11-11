"""Replay the desktop emergency live control without an encoder or platform."""

import argparse
from pathlib import Path
import tempfile

from playwright.sync_api import expect, sync_playwright

from test_local_home_ui import assert_no_horizontal_overflow, fulfill_json, home_tab, open_configuration, start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-live-stop-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                for width, theme in ((1280, "light"), (1440, "dark")):
                    context = browser.new_context(viewport={"width": width, "height": 800}, color_scheme=theme)
                    page = context.new_page()
                    page.route("**/api/local/apps/catalog", fulfill_json({"applications": [{"id": "wechat", "name": "微信"}]}))
                    page.route("**/api/apps", fulfill_json({"items": []}))
                    session = None
                    attempted = []

                    def active(route):
                        fulfill_json({"active": session})(route)

                    def submit(route):
                        nonlocal session
                        payload = route.request.post_data_json
                        assert payload == {"action": "stop", "id": "synthetic-live"}
                        assert route.request.headers.get("x-csrf-token")
                        attempted.append(payload)
                        if len(attempted) == 1:
                            fulfill_json({"error": "本机暂时无法确认停止，请重试"}, 503)(route)
                            return
                        session = {**session, "state": "stopping", "local_stopped": True}
                        fulfill_json(session)(route)

                    page.route("**/api/local/live/active", active)
                    page.route("**/api/local/live", submit)
                    page.goto(origin)
                    page.locator("#login-password").fill("synthetic-home-owner")
                    page.locator("#login-submit").click()
                    page.locator("#home-view").wait_for(state="visible")
                    if page.locator("#initialize-button").is_visible():
                        page.locator("#initialize-button").click()
                        page.locator("#initialize-button").wait_for(state="hidden")
                    panel = page.get_by_role("region", name="直播控制")
                    expect(panel).to_be_hidden()
                    session = {"id": "synthetic-live", "state": "live", "local_stopped": False, "remote_ended": False}
                    expect(panel).to_contain_text("直播进行中", timeout=8000)
                    home_tab(page, "apps")
                    open_configuration(page)
                    panel = page.locator("#app-configuration").get_by_role("region", name="直播控制")
                    button = panel.get_by_role("button", name="停止直播", exact=True)
                    expect(button).to_be_enabled()
                    assert_no_horizontal_overflow(page, f"emergency stop {width}")
                    assert panel.evaluate("el => el.scrollWidth <= el.clientWidth + 1")
                    page.locator("#app-configuration").screenshot(path=str(output / f"live-stop-{width}-{theme}.png"))
                    button.click()
                    expect(panel).to_contain_text("本机暂时无法确认停止")
                    button.click()
                    expect(panel).to_contain_text("本机输出已停止，等待平台确认结束")
                    expect(button).to_be_hidden()
                    assert len(attempted) == 2
                    page.reload()
                    expect(page.get_by_role("region", name="直播控制")).to_contain_text("等待平台确认结束")
                    assert len(attempted) == 2, "reload must never repeat a stop request"
                    session = None
                    expect(page.get_by_role("region", name="直播控制")).to_be_hidden(timeout=9000)
                    context.close()
                browser.close()
        finally:
            stop(process)
    print("Desktop live stop replay passed with synthetic local and platform states.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
