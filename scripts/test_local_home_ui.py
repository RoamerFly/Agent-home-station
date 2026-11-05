"""Opt-in local home acceptance: synthetic data, real HTTP, zero model/WeChat calls."""

import argparse
import json
import os
from pathlib import Path
import queue
import re
import subprocess
import tempfile
import threading

from playwright.sync_api import sync_playwright


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
                assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "narrow overflow"
                page.screenshot(path=str(output / "paused-dark-375.png"), full_page=True)
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
                assert not errors, errors
                browser.close()
                print(json.dumps({"status": "passed", "model_calls": 0, "wechat_calls": 0, "scenes": ["login", "initialize", "pause", "stale revision", "resume", "logout", "restart", "375 dark"]}))
        finally:
            stop(process)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    verify(args.binary.resolve(), args.output.resolve())
