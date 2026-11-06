"""Windows publication workbench acceptance with a real local station and synthetic platform tasks."""

import argparse
import json
from pathlib import Path
import re
import tempfile
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import expect, sync_playwright

from test_local_home_ui import assert_no_horizontal_overflow, fulfill_json, start, stop


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-publication-ui-", dir=output) as directory:
        root = Path(directory).resolve()
        process, origin = start(binary, root / "host", root / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                context = browser.new_context(viewport={"width": 1280, "height": 860}, color_scheme="light")
                page = context.new_page()
                errors = []
                writes = []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.goto(origin)
                assert context.request.get(origin + "/api/local/publications?app_id=bilibili").status == 401
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#chat-view").wait_for(state="visible")
                listing = context.request.get(origin + "/api/local/publications?app_id=bilibili").json()
                assert listing["tasks"]["items"] == [] and listing["targets"] == []

                connection_url = origin + "/api/apps/bilibili/connections"
                assert [item["id"] for item in context.request.get(connection_url).json()["items"]] == ["public"]
                ready = False
                job_ids = {name: key * 64 for name, key in zip(
                    ("draft", "upload", "unknown", "accepted", "later"), "abcde"
                )}

                def job(name, state, actions, stage=None, confirmed=0, remote=""):
                    item = {"job_id": job_ids[name], "state": state, "updated_at": "2026-11-06T11:00:00Z",
                            "available_actions": actions, "remote_id": remote}
                    if stage:
                        item["progress"] = {"stage": stage, "confirmed_parts": confirmed, "total_parts": 3}
                    return item

                entries = {
                    "draft": job("draft", "draft", ["cancel", "submit"]),
                    "upload": job("upload", "awaiting_receipt", ["status", "cancel", "resume"], "uploading", 1),
                    "unknown": job("unknown", "awaiting_receipt", ["status", "cancel"], "posting", 3),
                    "accepted": job("accepted", "accepted", ["status"], "accepted", 3, "BV1aB2cD3e4F"),
                    "later": job("later", "cancelled", []),
                }
                paused_upload = []

                def publications(route):
                    request = route.request
                    if request.method == "GET":
                        params = parse_qs(urlparse(request.url).query)
                        assert params["app_id"] == ["bilibili"] and params["limit"] == ["6"]
                        after = int(params["after"][0])
                        items = [] if not ready else [entries["later"]] if after else [
                            entries[name] for name in ("draft", "upload", "unknown", "accepted")
                        ]
                        fulfill_json({"tasks": {"items": items, "next": 42, "has_more": ready and not after}})(route)
                        return
                    body = request.post_data_json
                    assert request.headers.get("x-csrf-token"), "CSRF header missing"
                    assert set(body) == {"action", "job_id"}
                    name = next(key for key, value in job_ids.items() if value == body["job_id"])
                    action = body["action"]
                    writes.append((name, action))
                    if name == "draft" and action == "submit":
                        entries[name] = job(name, "accepted", ["status"], "accepted", 3, "BV9aB2cD3e4F")
                    elif name == "upload" and action == "resume":
                        paused_upload.append(route)
                        return
                    elif name == "upload" and action == "cancel":
                        entries[name] = job(name, "cancelled", [], "uploading", 1)
                        fulfill_json(entries[name])(paused_upload.pop())
                    elif name == "unknown" and action == "status":
                        fulfill_json({"error": "测试回执服务暂不可用"}, 503)(route)
                        return
                    elif name == "unknown" and action == "cancel":
                        entries[name].update(cancel_requested=True, available_actions=["status"])
                    elif name == "accepted" and action == "status":
                        entries[name] = job(name, "published", [], "accepted", 3, "BV1aB2cD3e4F")
                    else:
                        raise AssertionError(f"unexpected publication action {name}/{action}")
                    fulfill_json(entries[name])(route)

                page.route(re.compile(r".*/api/local/publications(?:\?.*)?$"), publications)
                page.locator("#apps-tab").click()
                panel = page.locator("#bilibili-publication")
                expect(panel.get_by_text("尚未登记发布授权", exact=True)).to_be_visible()
                expect(panel.get_by_text("暂无 B 站发布任务。", exact=True)).to_be_visible()
                expect(panel.get_by_role("button", name="提交投稿")).to_have_count(0)
                settings = root / "host" / "apps" / "bilibili" / "publication-settings.json"
                settings.parent.mkdir(parents=True, exist_ok=True)
                settings.write_text(json.dumps({
                    "schema_version": 1, "enabled": True, "client_id": "synthetic-fixture-app",
                    "app_secret": "synthetic-fixture-secret", "access_token": "synthetic-fixture-token",
                    "account_id": "synthetic-fixture-account", "expires_at": "2030-01-01T00:00:00Z",
                }), encoding="utf-8")
                grant = context.request.get(connection_url)
                assert grant.ok and "synthetic-fixture-" not in grant.text()
                assert [(item["id"], item["configuration"]) for item in grant.json()["items"] if item["id"] == "publish"] == [("publish", "ready")]
                ready = True
                page.locator("#apps-refresh").click()
                expect(panel.get_by_text("发布授权已登记", exact=True)).to_be_visible()
                expect(panel.get_by_role("progressbar", name="已确认 1 / 3 段")).to_be_visible()
                unknown = panel.locator(f'[data-job="{job_ids["unknown"]}"]')
                expect(unknown.get_by_text("投稿结果待核对", exact=True)).to_be_visible()
                expect(unknown.get_by_role("button", name="继续任务")).to_have_count(0)
                panel.screenshot(path=str(output / "bilibili-publication-light.png"))

                def row(name):
                    return panel.locator(f'[data-job="{job_ids[name]}"]')

                row("draft").get_by_role("button", name="提交投稿").click()
                expect(row("draft").get_by_text("已受理", exact=True)).to_be_visible()
                row("upload").get_by_role("button", name="继续任务").click()
                expect(row("upload").get_by_text("正在处理原任务…")).to_be_visible()
                row("upload").get_by_role("button", name="停止后续").click()
                expect(row("upload").get_by_text("已停止", exact=True)).to_be_visible()
                unknown.get_by_role("button", name="查询状态").click()
                expect(panel.get_by_role("status")).to_have_text("测试回执服务暂不可用")
                unknown.get_by_role("button", name="停止后续").click()
                expect(unknown.get_by_text("已要求停止", exact=True)).to_be_visible()
                expect(unknown.get_by_role("button", name="停止后续")).to_have_count(0)
                row("accepted").get_by_role("button", name="查询状态").click()
                expect(row("accepted").get_by_text("已公开", exact=True)).to_be_visible()
                panel.get_by_role("button", name="下一页").click()
                expect(panel.get_by_text("第 2 页", exact=True)).to_be_visible()
                expect(row("later")).to_be_visible()
                panel.get_by_role("button", name="上一页").click()
                expect(panel.get_by_text("第 1 页", exact=True)).to_be_visible()
                expect(row("accepted")).to_be_visible()
                page.set_viewport_size({"width": 390, "height": 844})
                page.emulate_media(color_scheme="dark")
                assert_no_horizontal_overflow(page, "B 站发布工作台")
                assert panel.evaluate("""panel => {
                    const {left, right} = panel.getBoundingClientRect();
                    return Array.from(panel.querySelectorAll('button, progress')).every(node => {
                        const rect = node.getBoundingClientRect();
                        return rect.left >= left && rect.right <= right;
                    });
                }"""), "controls clipped by application card"
                panel.screenshot(path=str(output / "bilibili-publication-dark-390.png"))
                assert not errors, errors
                assert writes == [("draft", "submit"), ("upload", "resume"), ("upload", "cancel"),
                                  ("unknown", "status"), ("unknown", "cancel"), ("accepted", "status")], writes
                browser.close()
        finally:
            stop(process)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
    print("Bilibili publication workbench acceptance passed")
