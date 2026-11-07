"""Offline browser acceptance for the ComfyUI deployment registration dialog.

The browser intercepts its deployment endpoint.  No ComfyUI directory is
opened, no backend starts, and this test never sends messages or calls a model.
"""

import argparse
import json
from pathlib import Path
import re
import tempfile
import time

from playwright.sync_api import expect, sync_playwright

from test_local_home_ui import assert_no_horizontal_overflow, start, stop


def fulfill(route, payload, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(payload, ensure_ascii=False))


def comfy_tool(deployment):
    return {
        "id": "comfyui", "name": "ComfyUI 图像工作流", "catalog_available": True,
        "runtime_ready": deployment["configured"], "installed": False, "enabled": False,
        "revision": 1, "state": "idle", "reason": "" if deployment["configured"] else "comfyui_backend_not_configured", "workflows": [],
    }


def verify(binary, output):
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="synthetic-comfy-setup-ui-", dir=output) as directory:
        root = Path(directory).resolve()
        process, origin = start(binary, root / "host", root / "workspace")
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(channel="msedge", headless=True)
                context = browser.new_context(viewport={"width": 1120, "height": 820}, color_scheme="light")
                page = context.new_page()
                server = {"revision": "unconfigured", "configured": False, "read_only": False, "configuration": None}
                writes, conflict, delay_get, delay_put = [], False, False, False
                expected_models = r"E:\Models\ComfyUI"

                def project_tools(route):
                    assert route.request.method == "GET"
                    fulfill(route, {"tools": [comfy_tool(server)]})

                def deployment(route):
                    nonlocal server, conflict, delay_get, delay_put, expected_models
                    if route.request.method == "GET":
                        if delay_get:
                            time.sleep(.25)
                            delay_get = False
                        fulfill(route, server)
                        return
                    assert route.request.headers.get("x-csrf-token"), "deployment save needs CSRF"
                    body = route.request.post_data_json
                    writes.append(body)
                    assert body["approve_execution"] is True
                    assert body["revision"] == server["revision"]
                    if conflict:
                        fulfill(route, {"error": "部署登记已更新"}, 409)
                        return
                    configuration = body["configuration"]
                    assert configuration["schema_version"] == 1 and configuration["enabled"] is True
                    assert configuration["runtime"] == r"E:\Tools\ComfyUI", repr(configuration)
                    assert configuration["models"] == expected_models, repr(configuration)
                    assert configuration["workflows"] == r"E:\Workflows\ComfyUI", repr(configuration)
                    assert configuration["manifest"] == "approved-manifest.json"
                    assert configuration["idle_seconds"] == 0, repr(configuration)
                    if delay_put:
                        time.sleep(.25)
                        delay_put = False
                    server = {"revision": "r1", "configured": True, "read_only": False, "configuration": configuration}
                    fulfill(route, server)

                page.route(re.compile(r".*/api/local/project-tools/comfyui/deployment$"), deployment)
                page.route(re.compile(r".*/api/local/project-tools$"), project_tools)
                page.goto(origin)
                page.locator("#login-password").fill("synthetic-home-owner")
                page.locator("#login-submit").click()
                page.locator("#initialize-button").click()
                page.locator("#project-tools-tab").click()
                page.get_by_role("button", name="管理ComfyUI 图像工作流", exact=True).click()
                dialog = page.locator("#tool-dialog")
                expect(dialog.get_by_text("本机部署登记", exact=True)).to_be_visible()
                runtime = dialog.get_by_label("ComfyUI 安装目录")
                expect(runtime).to_be_visible()
                runtime.fill(r"E:\Tools\ComfyUI")
                dialog.get_by_label("模型目录").fill(r"E:\Models\ComfyUI")
                dialog.get_by_label("工作流目录").fill(r"E:\Workflows\ComfyUI")
                dialog.get_by_label("审核清单").fill("approved-manifest.json")
                dialog.get_by_text("运行细节", exact=True).click()
                dialog.get_by_label("闲置停止秒数").fill("0")
                runtime.press("Enter")
                page.wait_for_timeout(80)
                assert not writes, "Enter without approval must not submit deployment"
                dialog.get_by_role("button", name="重新读取", exact=True).click()
                expect(runtime).to_have_value(r"E:\Tools\ComfyUI")
                dialog.get_by_label("确认这些运行文件和工作流已审核").check()
                with page.expect_response(lambda response: response.url.endswith("/comfyui/deployment") and response.request.method == "PUT"):
                    dialog.get_by_role("button", name="保存部署", exact=True).click()
                expect(dialog.get_by_text("部署登记已保存；工具仍需单独启用。", exact=True)).to_be_visible()
                assert len(writes) == 1
                page.locator("#project-tools-list[aria-busy='false']").wait_for()
                dialog.screenshot(path=str(output / "comfy-deployment-desktop-light.png"))
                conflict = True
                models = dialog.get_by_label("模型目录")
                models.fill(r"E:\Models\ComfyUI-draft")
                with page.expect_response(lambda response: response.url.endswith("/comfyui/deployment") and response.status == 409):
                    dialog.get_by_role("button", name="保存部署", exact=True).click()
                expect(dialog.get_by_text("部署登记已更新", exact=True)).to_be_visible()
                expect(models).to_have_value(r"E:\Models\ComfyUI-draft")
                conflict = False
                expected_models = r"E:\Models\ComfyUI-final"
                models.fill(expected_models)
                delay_put = True
                dialog.get_by_role("button", name="保存部署", exact=True).click()
                page.locator("#tool-dialog-close").click()
                page.wait_for_timeout(350)
                page.get_by_role("button", name="管理ComfyUI 图像工作流", exact=True).click()
                dialog = page.locator("#tool-dialog")
                expect(dialog.get_by_label("模型目录")).to_have_value(expected_models)
                delay_get = True
                dialog.get_by_role("button", name="重新读取", exact=True).click()
                page.locator("#tool-dialog-close").click()
                page.wait_for_timeout(350)
                page.get_by_role("button", name="管理ComfyUI 图像工作流", exact=True).click()
                dialog = page.locator("#tool-dialog")
                expect(dialog.get_by_label("模型目录")).to_have_value(expected_models)
                page.locator("#tool-dialog-body").evaluate("node => { node.scrollTop = 0; }")
                page.set_viewport_size({"width": 390, "height": 760})
                page.emulate_media(color_scheme="dark")
                dialog.screenshot(path=str(output / "comfy-deployment-narrow-dark.png"))
                assert_no_horizontal_overflow(page, "ComfyUI deployment narrow dark")
                actions = dialog.locator(".comfy-deployment-actions")
                actions.scroll_into_view_if_needed()
                approval_box = dialog.get_by_label("确认这些运行文件和工作流已审核").bounding_box()
                assert approval_box and 14 <= approval_box["width"] <= 20 and 14 <= approval_box["height"] <= 20, "approval checkbox must remain compact"
                actions.screenshot(path=str(output / "comfy-deployment-actions-narrow-dark.png"))
                server = {**server, "read_only": True, "reason": "请先停用 ComfyUI，再修改运行环境。"}
                dialog.get_by_role("button", name="重新读取", exact=True).click()
                expect(dialog.get_by_text("请先停用 ComfyUI，再修改运行环境。", exact=True)).to_be_visible()
                expect(dialog.get_by_role("button", name="保存部署", exact=True)).to_be_disabled()
                browser.close()
        finally:
            stop(process)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    verify(arguments.binary.resolve(), arguments.output.resolve())
