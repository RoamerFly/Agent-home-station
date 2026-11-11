"""Replay the Windows WeChat relocation UI with synthetic account responses."""

import argparse
from pathlib import Path
import tempfile

from playwright.sync_api import expect, sync_playwright

from test_local_home_ui import app_connection_payload, assert_no_horizontal_overflow, fulfill_json, home_tab, open_configuration, start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-relocation-", dir=output) as directory:
        root = Path(directory)
        process, origin = start(binary, root / "host", root / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                for width, color in ((1280, "light"), (375, "dark")):
                    context = browser.new_context(viewport={"width": width, "height": 800}, color_scheme=color)
                    page = context.new_page()
                    page.route("**/api/local/apps/catalog", fulfill_json({"applications": [{"id": "wechat", "name": "微信", "install_state": "installed"}]}))
                    page.route("**/api/apps", fulfill_json(app_connection_payload()))
                    base = "**/api/apps/wechat/connections"
                    page.route(base, fulfill_json({"items": [
                        {"id": "synthetic-receiver", "label": "搬迁账号"},
                        {"id": "synthetic-regular", "label": "普通账号"},
                    ]}))
                    blocked = True
                    confirmed = False
                    stale = True
                    calls = []
                    actions = []
                    account = {"login": "authenticated", "configured_policy": {"conversation_enabled": True}}
                    page.route(base + "/synthetic-regular", fulfill_json({"connection": {**account, "id": "synthetic-regular", "worker": "paused"}}))
                    receiver = base + "/synthetic-receiver"

                    def inspect(route):
                        row = {**account, "id": "synthetic-receiver", "worker": "paused" if confirmed else "rollback_frozen"}
                        if not confirmed:
                            row["worker_blocker"] = "relocation_confirmation_required"
                        fulfill_json({"connection": row})(route)

                    def readiness(route):
                        nonlocal confirmed
                        request = route.request
                        calls.append(request.method)
                        if request.method == "GET":
                            if blocked:
                                fulfill_json({"state": "blocked", "reason": "源站封存记录暂不可核对", "frozen": True})(route)
                            else:
                                fulfill_json({"state": "owner_identity_check", "digest": "a" * 64,
                                              "source_fence": "verified", "native_login": "online", "frozen": True})(route)
                        else:
                            assert request.post_data_json == {"digest": "a" * 64, "same_identity": True}
                            assert request.headers.get("x-csrf-token")
                            if stale:
                                fulfill_json({"state": "blocked", "reason": "接收站启用条件已变化，请重新核对"}, 409)(route)
                            else:
                                confirmed = True
                                fulfill_json({"state": "confirmed", "frozen": False})(route)

                    def lifecycle(route):
                        actions.append(route.request.post_data_json["action"])
                        fulfill_json({})(route)

                    page.route(receiver, inspect)
                    page.route(receiver + "/legacy-import/relocation/readiness", readiness)
                    page.route(receiver + "/lifecycle", lifecycle)
                    page.goto(origin)
                    page.locator("#login-password").fill("synthetic-home-owner")
                    page.locator("#login-submit").click()
                    page.locator("#home-view").wait_for(state="visible")
                    if page.locator("#initialize-button").is_visible():
                        page.locator("#initialize-button").click()
                        page.locator("#initialize-button").wait_for(state="hidden")
                    home_tab(page, "apps")
                    page.locator("#apps-list[aria-busy='false']").wait_for()
                    open_configuration(page)
                    dialog = page.locator("#app-configuration")
                    panel = dialog.get_by_role("region", name="微信搬迁核对")
                    expect(panel).to_have_count(1)
                    assert not calls
                    expect(dialog.get_by_role("button", name="启动 Agent", exact=True).first).to_be_disabled()
                    panel.get_by_role("button", name="核对搬迁状态").click()
                    expect(panel).to_contain_text("源站封存记录暂不可核对")
                    expect(panel.get_by_role("button", name="确认搬迁")).to_have_count(0)
                    blocked = False
                    panel.get_by_role("button", name="核对搬迁状态").click()
                    confirm = panel.get_by_role("button", name="确认搬迁")
                    expect(confirm).to_be_disabled()
                    panel.locator("xpath=ancestor::li").get_by_text("搬迁账号", exact=True).scroll_into_view_if_needed()
                    assert_no_horizontal_overflow(page, f"WeChat relocation {width}")
                    assert panel.evaluate("e => e.scrollWidth <= e.clientWidth + 1"), "relocation panel overflows"
                    dialog.screenshot(path=str(output / f"wechat-relocation-{width}-{color}.png"))
                    panel.get_by_role("checkbox").check()
                    confirm.click()
                    expect(panel).to_contain_text("接收站启用条件已变化")
                    expect(confirm).to_have_count(0)
                    assert not actions
                    stale = False
                    panel.get_by_role("button", name="核对搬迁状态").click()
                    panel.get_by_role("checkbox").check()
                    confirm.click()
                    expect(panel).to_have_count(0)
                    expect(dialog).to_contain_text("微信搬迁已确认，Agent 仍暂停")
                    assert calls == ["GET", "GET", "POST", "GET", "POST"] and not actions
                    dialog.get_by_role("button", name="启动 Agent", exact=True).first.click()
                    expect(dialog).to_contain_text("已启动此账号的 Agent")
                    assert actions == ["start"]
                    context.close()
                browser.close()
        finally:
            stop(process)
    print("WeChat relocation UI replay passed with synthetic account responses.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
