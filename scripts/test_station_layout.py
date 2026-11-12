"""Station layout and application persona replay, synthetic data and no model calls."""
import argparse
import json
import re
from pathlib import Path
import tempfile
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import sync_playwright, expect
from test_local_home_ui import start, stop, open_application, close_application, home_tab


def bounds(page):
    return page.evaluate("""() => {
      const r=document.querySelector('.home-visual').getBoundingClientRect();
      return {top:r.top,bottom:r.bottom,height:r.height,root:document.scrollingElement.scrollTop,
        documentHeight:document.documentElement.scrollHeight,viewport:innerHeight,
        overflow:document.documentElement.scrollWidth>innerWidth};
    }""")


def fixed_shell(page, expected=None):
    value=bounds(page)
    assert not value['overflow'], value
    assert value['documentHeight']<=value['viewport']+1 and value['root']==0, value
    assert value['top']>=0 and value['bottom']<=value['viewport'],value
    if expected is not None: assert abs(value['height']-expected['height'])<1, (expected,value)
    return value



def verify_glass_views(page, output):
    # Only this visual fixture intercepts reads. The owner API itself is
    # exercised separately by TestLocalMemoryPalaceOwnerScopeAndSources.
    entries = [
        {"id": "meeting", "title": "周日画明信片", "kind": "episode", "commitment": "open"},
        {"id": "diary", "title": "海边日记", "kind": "episode"},
        {"id": "sea", "title": "夜海", "kind": "preference"},
        {"id": "fish", "title": "大肥鱼", "kind": "fact"},
    ]

    def palace_response(route):
        request = urlparse(route.request.url)
        if request.path.endswith("/palace"):
            kind = parse_qs(request.query).get("kind", [""])[0]
            items = [entry for entry in entries if not kind or
                     (entry.get("commitment") if kind == "commitment" else entry["kind"] == kind)]
            route.fulfill(json={"items": items})
        else:
            item = next(entry for entry in entries if request.path.endswith("/" + entry["id"]))
            route.fulfill(json={"memory": item, "detail": item["title"], "version": 2,
                                "sources": [{"quote": "周日一起画明信片。", "at": "2026-11-07T12:00:00Z"}],
                                "revisions": [{"version": 1, "title": "一起画明信片"}]})

    palace = re.compile(r"/api/local/memory/palace(?:[/?]|$)")
    chat = "**/api/local/chat?*"
    page.route(palace, palace_response)
    page.route(chat, lambda route: route.fulfill(json={"items": [
        {"id": "synthetic-owner", "role": "user", "text": "今晚整理海边日记。", "time": "2026-11-07T12:00:00Z"},
        {"id": "synthetic-fish", "role": "assistant", "text": "我把那段经历留在记忆宫殿里了。", "time": "2026-11-07T12:01:00Z"},
    ]}))
    home_tab(page, "chat")
    expect(page.locator(".chat-bubble")).to_have_count(2)
    expect(page.locator("#recent-memory-items")).to_contain_text("海边日记")
    page.screenshot(path=str(output/"01-chat.png"))

    page.locator("#open-memory-button").click()
    expect(page.locator("#memory-graph .memory-node")).to_have_count(4)
    expect(page.locator("#memory-detail")).to_contain_text("周日一起画明信片。")
    expect(page.locator("#memory-detail")).to_contain_text("版本 1")
    page.screenshot(path=str(output/"02-memory-palace.png"))
    page.locator("#memory-graph").get_by_role("button", name="夜海 兴趣").click()
    page.get_by_role("button", name="列表", exact=True).click()
    expect(page.locator("#memory-list [aria-pressed=true]")).to_contain_text("夜海")
    page.locator("#memory-filters").get_by_role("button", name="约定", exact=True).click()
    expect(page.locator("#memory-list .memory-row")).to_have_count(1)
    page.set_viewport_size({"width": 1280, "height": 740})
    fixed_shell(page)
    expect(page.locator("#memory-detail")).to_contain_text("周日一起画明信片。")
    page.screenshot(path=str(output/"memory-desktop-small.png"))
    page.get_by_role("button", name="返回原会话", exact=True).click()
    page.set_viewport_size({"width": 1440, "height": 900})
    expect(page.locator("#chat-tab")).to_be_focused()
    page.unroute(palace)
    page.unroute(chat)

    home_tab(page, "apps")
    expect(page.locator("#apps-list .app-record")).to_have_count(2)
    page.locator("#apps-search").fill("抖音")
    expect(page.locator("#apps-list .app-record")).to_have_count(1)
    page.locator("#apps-search").clear()
    page.get_by_role("button", name="管理微信", exact=True).click()
    expect(page.locator("#app-dialog-tabs button")).to_have_text(["概览", "账号与权限", "应用能力", "资料与版本"])
    expect(page.locator("#app-dialog-body")).to_contain_text("记忆接续")
    page.screenshot(path=str(output/"03-app-center-wechat.png"))
    page.get_by_role("button", name="管理抖音", exact=True).click()
    expect(page.locator("#app-dialog-tabs [aria-pressed=true]")).to_have_text("应用能力")
    expect(page.locator("#app-dialog-body")).to_contain_text("直播控制")
    expect(page.locator("#app-dialog-body")).to_contain_text("待验证")
    page.screenshot(path=str(output/"04-app-center-douyin.png"))
    page.keyboard.press("Escape")
    expect(page.get_by_role("button", name="管理抖音", exact=True)).to_be_focused()


def verify_memory_source_selection(page, output):
    source = {"connection_id": "wechat-synthetic", "account_id": "synthetic-owner",
              "thread_id": "synthetic-memory@chatroom", "dataset": "archive"}
    requested = []
    pending = []
    pattern = re.compile(r"/api/local/memory/palace(?:[/?]|$)")

    def palace(route):
        url = urlparse(route.request.url)
        query = parse_qs(url.query)
        if url.path.endswith("/sources"):
            requested.append("sources")
            route.fulfill(json={"app_id": "wechat", "sources": [source]})
            return
        if query.get("app_id") == ["wechat"]:
            for field, value in source.items():
                assert query[field] == [value]
            if url.path.endswith("/archive-memory"):
                assert query["scope_version"] == ["synthetic-version"]
                if "source_index" in query:
                    requested.append("original")
                    if "offset" in query and query["offset"] != ["0"]:
                        pending.append(route)
                        return
                    route.fulfill(json={"detail": "合成封存原话 <img src=invalid>", "next_offset": 40, "has_more": True})
                    return
                requested.append("detail")
                route.fulfill(json={"memory": {"id": "archive-memory", "title": "合成微信档案", "kind": "fact", "status": "archived"},
                                    "detail": "合成封存提炼记忆", "version": 1, "record_origin": "legacy_import_snapshot", "has_more": False,
                                    "sources": [{"source_index": 0, "message_id": "synthetic-original", "at": "2026-01-01T00:00:00Z"}], "revisions": []})
                return
            requested.append("list")
            route.fulfill(json={"items": [{"id": "archive-memory", "title": "合成微信档案", "kind": "fact", "status": "archived"}],
                                "scope_version": "synthetic-version", "next_cursor": ""})
        else:
            route.fulfill(json={"items": [], "next_cursor": ""})

    page.route(pattern, palace)
    home_tab(page, "memory")
    page.locator("#memory-modes").get_by_role("button", name="关系图", exact=True).click()
    expect(page.locator("#memory-source-label")).not_to_be_visible()
    assert not requested
    page.locator("#memory-source-load").click()
    select = page.get_by_label("记忆来源", exact=True)
    expect(select).to_be_visible()
    assert requested == ["sources"]
    select.select_option("0")
    node = page.locator("#memory-graph").get_by_role("button", name="合成微信档案 人物与事实", exact=True)
    expect(node).to_be_visible()
    expect(page.locator("#memory-detail")).not_to_contain_text("合成封存提炼记忆")
    assert requested == ["sources", "list"]
    node.click()
    panel = page.locator("#memory-detail")
    expect(panel).to_contain_text("合成封存提炼记忆")
    expect(panel).not_to_contain_text("合成封存原话")
    assert requested == ["sources", "list", "detail"]
    panel.get_by_role("button", name="读取来源原话", exact=True).click()
    expect(panel).to_contain_text("合成封存原话 <img src=invalid>")
    expect(panel.locator("img")).to_have_count(0)
    page.set_viewport_size({"width": 1280, "height": 740})
    fixed_shell(page)
    assert page.locator("#memory-view").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    panel.scroll_into_view_if_needed()
    page.screenshot(path=str(output / "memory-wechat-archive-desktop-small.png"))
    panel.get_by_role("button", name="继续读取原文", exact=True).click()
    expect(panel.get_by_role("button", name="继续读取原文", exact=True)).to_have_count(0)
    assert len(pending) == 1
    select.select_option("")
    expect(panel).not_to_contain_text("合成封存原话")
    pending.pop().fulfill(json={"detail": "迟到的微信原话", "has_more": False})
    expect(panel).not_to_contain_text("迟到的微信原话")
    expect(page.locator("#memory-scope-label")).to_have_text("本机会话")
    page.set_viewport_size({"width": 1440, "height": 900})
    page.unroute(pattern)


def verify_douyin_read_controls(page, panel, nav):
    observed = []
    pattern = re.compile(r"/api/local/apps/observation(?:\?|$)")

    def observation(route):
        req = route.request
        if req.method == "GET":
            assert parse_qs(urlparse(req.url).query) == {"app_id": ["douyin"]}
            observed.append("discover")
            route.fulfill(json={"items": [{"app_id": "douyin", "connection_id": "synthetic-web",
                                           "content_id": "home", "fields": ["page_text"]},
                                          {"app_id": "douyin", "connection_id": "synthetic-web",
                                           "content_id": "account-works", "fields": ["account_works"]}]})
            return
        payload = req.post_data_json
        field = payload["field"]
        assert payload["app_id"] == "douyin" and payload["connection_id"] == "synthetic-web"
        observed.append(field)
        if field == "account_works":
            assert payload["content_id"] == "account-works"
            route.fulfill(json={"coverage": "已登录创作者管理页", "complete": True, "chunks": [
                {"text": "合成本人作品", "follow_up_content_id": "creator-work-synthetic",
                 "follow_up_fields": ["page_text", "images"]},
            ]})
        elif field == "page_text":
            if payload["content_id"] == "creator-work-synthetic":
                route.fulfill(json={"coverage": "创作者管理页快照", "complete": True, "chunks": [
                    {"text": "合成本人作品简介"},
                ]})
            else:
                assert payload["content_id"] == "home"
                route.fulfill(json={"coverage": "首页文字样本", "complete": True, "chunks": [
                    {"text": "合成视频候选", "follow_up_content_id": "synthetic-video",
                     "follow_up_fields": ["comments", "visual"]},
                ]})
        elif field == "images":
            assert payload["content_id"] == "creator-work-synthetic"
            second = observed.count("images") == 2
            route.fulfill(json={"coverage": "第 3 张，共 3 张" if second else "第 1–2 张，共 3 张",
                                "complete": second, "resumed": second,
                                "chunks": [{"text": "合成第三张图" if second else "合成图文描述"}]})
        else:
            assert payload["content_id"] == "synthetic-video"
            if field == "comments":
                route.fulfill(json={"coverage": "本次评论覆盖", "complete": True,
                                    "chunks": [{"text": "合成评论原话"}]})
            else:
                route.fulfill(json={"coverage": "采样不代表完整观看", "complete": True,
                                    "visual_proof": {"start_frame": 0, "next_frame": 6},
                                    "chunks": [{"text": "合成画面分析"}]})

    page.route(pattern, observation)
    nav.get_by_role("button", name="内容浏览", exact=True).click()
    expect(panel.get_by_role("button", name="查看可读取范围")).to_be_visible()
    assert not observed
    panel.get_by_role("button", name="查看可读取范围").click()
    panel.get_by_role("button", name="网页文字", exact=True).click()
    panel.get_by_role("button", name="评论区", exact=True).click()
    expect(panel.get_by_text("合成评论原话", exact=True)).to_be_visible()
    panel.get_by_role("button", name="视频画面", exact=True).click()
    expect(panel.get_by_text("采样不代表完整观看", exact=True)).to_be_visible()
    expect(panel.get_by_text("合成画面分析", exact=True)).to_be_visible()
    panel.get_by_role("button", name="本人作品列表", exact=True).click()
    panel.get_by_role("button", name="本人作品信息", exact=True).click()
    expect(panel.get_by_text("合成本人作品简介", exact=True)).to_be_visible()
    panel.get_by_role("button", name="图文图片", exact=True).click()
    expect(panel.get_by_text("合成图文描述", exact=True)).to_be_visible()
    panel.get_by_role("button", name="继续读取", exact=True).click()
    expect(panel.get_by_text("合成第三张图", exact=True)).to_be_visible()
    assert observed == ["discover", "page_text", "comments", "visual", "account_works", "page_text", "images", "images"]
    page.unroute(pattern, observation)


def verify_douyin_notice(page, panel, nav):
    status_path = "**/api/local/apps/douyin/web-login"
    notice_path = "**/api/local/apps/douyin/web-notice"
    observed = []
    login = {"revision": 7, "state": "saved", "active": False, "available": True, "notice_bound": False, "notice_agent_enabled": False}

    def status(route):
        if route.request.method == "POST":
            data = route.request.post_data_json
            assert data["action"] == "set_notice_agent" and data["revision"] == login["revision"]
            assert login["notice_bound"] is True
            login["notice_agent_enabled"] = data["allow_notice_agent"]
            login["revision"] += 1
            observed.append("agent_enable" if data["allow_notice_agent"] else "agent_disable")
        route.fulfill(json=login)

    def notice(route):
        data = route.request.post_data_json
        action = data["action"]
        observed.append(action)
        assert data["revision"] == login["revision"]
        if action in ("bind", "revoke"):
            assert "allow_mark_read" not in data
            login["notice_bound"] = action == "bind"
            login["notice_agent_enabled"] = False
            login["revision"] += 1
            route.fulfill(json={"revision": login["revision"], "notice_bound": login["notice_bound"], "notice_agent_enabled": False})
        elif action == "prepare":
            assert data["allow_mark_read"] is True
            assert data["pages"] == 2
            assert data["continuation"] == ("synthetic-next" if observed.count("read") else "")
            route.fulfill(json={"ticket": "synthetic-single-use"})
        else:
            assert action == "read" and data["allow_mark_read"] is True and data["ticket"] == "synthetic-single-use"
            resumed = observed.count("read") == 2
            assert data["pages"] == 2 and data["continuation"] == ("synthetic-next" if resumed else "")
            route.fulfill(json={"items": [{"id": "1233" if resumed else "1234", "type": 2, "work_id": "5678"}], "has_more": not resumed,
                                "pages_read": 1, "effective_pages": 2, "continuation": "" if resumed else "synthetic-next", "progress": "next_unavailable" if not resumed else "",
                                "rendered_text": "合成用户评论了你的作品 <img src=invalid>"})

    page.route(status_path, status)
    page.route(notice_path, notice)
    nav.get_by_role("button", name="互动任务", exact=True).click()
    bind = panel.get_by_role("button", name="绑定当前抖音账号", exact=True)
    expect(bind).to_be_visible()
    assert not observed
    expect(panel.get_by_role("button", name="读取并标记已读", exact=True)).to_have_count(0)
    bind.click()
    read = panel.get_by_role("button", name="读取并标记已读", exact=True)
    expect(read).to_be_visible()
    assert observed == ["bind"]
    enable_agent = panel.get_by_role("button", name="允许 Agent 读取并标记已读", exact=True)
    enable_agent.click()
    disable_agent = panel.get_by_role("button", name="关闭 Agent 通知读取", exact=True)
    expect(disable_agent).to_be_visible()
    assert observed == ["bind", "agent_enable"]
    panel.get_by_label("通知读取批数", exact=True).select_option("2")
    read.click()
    expect(panel.get_by_text("通知 1234 · 平台类型 2", exact=True)).to_be_visible()
    expect(panel.get_by_text("合成用户评论了你的作品 <img src=invalid>", exact=True)).to_be_visible()
    expect(panel.get_by_text("本次读取 1/2 批，返回 1 条新通知。平台报告还有后续通知。后批未能继续加载，已返回本次成功读取的部分。")).to_be_visible()
    assert observed == ["bind", "agent_enable", "prepare", "read"]
    resume = panel.get_by_role("button", name="继续读取并标记已读", exact=True)
    resume.click()
    expect(panel.get_by_text("通知 1233 · 平台类型 2", exact=True)).to_be_visible()
    expect(panel.get_by_text("本次读取 1/2 批，返回 1 条新通知。平台报告已读到末批。")).to_be_visible()
    expect(resume).not_to_be_visible()
    assert observed == ["bind", "agent_enable", "prepare", "read", "prepare", "read"]
    disable_agent.click()
    expect(enable_agent).to_be_visible()
    assert observed[-1] == "agent_disable"
    panel.get_by_role("button", name="撤销通知绑定", exact=True).click()
    expect(bind).to_be_visible()
    expect(panel.get_by_text("通知 1234 · 平台类型 2", exact=True)).to_have_count(0)
    assert observed[-1] == "revoke"
    page.unroute(status_path)
    page.unroute(notice_path)


def verify_douyin_memory_permissions(page, panel, nav, output):
    status_path = "**/api/local/apps/douyin/web-login"
    status = {"revision": 30, "state": "saved", "active": False, "available": True, "works_enabled": True,
              "comments_enabled": True, "notice_bound": True, "notice_agent_enabled": False, "memory_scopes": []}
    writes = []

    def login(route):
        if route.request.method == "POST":
            data = route.request.post_data_json
            assert data["action"] == "set_memory" and data["revision"] == status["revision"]
            scope, enabled = data["memory_scope"], data["allow_memory"]
            writes.append((scope, enabled))
            if enabled:
                status["memory_scopes"].append(scope)
            else:
                status["memory_scopes"].remove(scope)
            status["revision"] += 1
        route.fulfill(json=status)

    page.route(status_path, login)
    nav.get_by_role("button", name="账号与授权", exact=True).click()
    works = panel.get_by_role("checkbox", name="本人作品文字", exact=True)
    comments = panel.get_by_role("checkbox", name="本人作品评论", exact=True)
    notices = panel.get_by_role("checkbox", name="互动通知文字与元数据", exact=True)
    expect(works).to_be_enabled()
    expect(works).not_to_be_checked()
    expect(comments).not_to_be_checked()
    expect(notices).to_be_disabled()
    assert not writes
    works.check()
    expect(works).to_be_checked()
    expect(works).to_be_enabled()
    comments.check()
    expect(comments).to_be_checked()
    expect(works).to_be_enabled()
    page.screenshot(path=str(output / "douyin-memory-grants-desktop-small.png"))
    works.uncheck()
    expect(works).not_to_be_checked()
    expect(comments).to_be_checked()
    assert writes == [("works", True), ("comments", True), ("works", False)]
    page.unroute(status_path)


def verify_wechat_owner_binding(page, panel, nav, output):
    reads, writes = [], []
    memory_writes = []
    cross_writes = []
    station_writes = []
    douyin_writes = []
    cross_pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/cross-memory-grants$")
    station_pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/station-memory-grants$")
    cross_states = {key: {"revision": 0, "owner_revision": 0, "sources": {}, "origins": {}}
                    for key in ("wechat-synthetic-one", "wechat-synthetic-two")}
    station_states = {key: dict(value) for key, value in cross_states.items()}
    douyin_states = {key: {**value, "app_sources": {}} for key, value in cross_states.items()}
    douyin_pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/douyin-memory-grants$")
    def cross_grants(route):
        path = urlparse(route.request.url).path
        connection = path.split("/")[-2]
        is_douyin = path.endswith("/douyin-memory-grants")
        is_station = path.endswith("/station-memory-grants") or is_douyin
        state, owner_state = (douyin_states if is_douyin else station_states if is_station else cross_states)[connection], saved[connection]
        if route.request.method == "PUT":
            body = route.request.post_data_json
            assert route.request.headers.get("x-csrf-token")
            assert body["revision"] == state["revision"] and body["request_key"]
            assert not body["origins"] or body["owner_revision"] == owner_state["revision"]
            assert not is_station or not body["sources"]
            (douyin_writes if is_douyin else station_writes if is_station else cross_writes).append(body)
            if is_douyin:
                assert "app_sources" in body
                state["app_sources"] = {kind: "synthetic-" + kind for kind in body["app_sources"]}
            state.update(revision=state["revision"] + 1, owner_revision=owner_state["revision"],
                         sources={key: "2026-01-01T00:00:00Z" for key in body["sources"]},
                         origins={key: "2026-01-01T00:00:00Z" for key in body["origins"]})
        else:
            assert route.request.method == "GET"
        route.fulfill(json={**state, "current_owner_revision": owner_state["revision"],
                            "scopes_current": state["owner_revision"] == owner_state["revision"] and bool(state["origins"]),
                            "owner_bound": bool(owner_state.get("binding")), "private_reply_authorized": bool(owner_state.get("binding")),
                            "available_sources": ["memory-b@chatroom"] if connection.endswith("one") and not is_station else [],
                            "available_origins": ["memory-a@chatroom"] if connection.endswith("one") else [],
                            "available_app_sources": [{"kind": "works", "source_id": "synthetic-works"}, {"kind": "notices", "source_id": "synthetic-notices"}]
                                if is_douyin and connection.endswith("one") else []})
    page.route(cross_pattern, cross_grants)
    page.route(station_pattern, cross_grants)
    page.route(douyin_pattern, cross_grants)
    memory_pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/memory-grants$")
    grants = {"wechat-synthetic-one": {"revision": 0, "station_reads": {}, "available_history_threads": ["memory-a@chatroom", "memory-b@chatroom"]},
              "wechat-synthetic-two": {"revision": 0, "station_reads": {}, "available_history_threads": []}}
    def memory_grants(route):
        state = grants[urlparse(route.request.url).path.split("/")[-2]]
        if route.request.method == "PUT":
            body = route.request.post_data_json
            assert body["revision"] == state["revision"] and body["request_key"]
            assert route.request.headers.get("x-csrf-token")
            memory_writes.append(body)
            state["station_reads"] = {thread: "2026-01-01T00:00:00Z" for thread in body["station_reads"]}
            state["revision"] += 1
        else:
            assert route.request.method == "GET"
        route.fulfill(json=state)
    page.route(memory_pattern, memory_grants)
    saved = {"wechat-synthetic-one": {"account_id": "one", "revision": 0},
             "wechat-synthetic-two": {"account_id": "two", "revision": 0}}
    pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/owner-binding$")
    def binding(route):
        request = route.request
        connection = urlparse(request.url).path.split("/")[-2]
        state = saved[connection]
        if request.method == "GET":
            reads.append(connection)
            route.fulfill(json=state)
            return
        assert request.method == "POST" and request.headers.get("x-csrf-token")
        body = request.post_data_json
        writes.append(body)
        assert body["revision"] == state["revision"]
        if body["action"] == "preview":
            route.fulfill(json={"account_id": state["account_id"], "sender_id": "wxid-synthetic-owner",
                "thread_id": "synthetic-group@chatroom", "message_id": body["message_id"], "digest": "synthetic-digest", "revision": state["revision"]})
            return
        if body["action"] == "bind":
            assert body["same_identity"] is True and body["digest"] == "synthetic-digest" and body["request_key"]
            state["binding"] = {"sender_id": "wxid-synthetic-owner"}
            state["revision"] += 1
        elif body["action"] == "check":
            route.fulfill(json={"is_bound_owner": body["message_id"] == "owner-message", "revision": state["revision"]})
            return
        elif body["action"] == "revoke":
            state.pop("binding", None)
            state["revision"] += 1
        else:
            raise AssertionError(body)
        route.fulfill(json=state)
    page.route(pattern, binding)
    assert not reads and not writes
    nav.get_by_role("button", name="账号与授权", exact=True).click()
    owner = panel.locator(".configuration-owner-binding")
    owner = owner.filter(has=page.get_by_role("heading", name="微信本人身份", exact=True))
    expect(owner.get_by_text("尚未绑定微信本人", exact=True)).to_be_visible()
    memory = panel.locator(".configuration-owner-binding").filter(has=page.get_by_role("heading", name="工作站按需查微信", exact=True))
    cross = panel.locator(".configuration-owner-binding").filter(has=page.get_by_role("heading", name="微信本人跨群查询", exact=True))
    station_memory = panel.locator(".configuration-owner-binding").filter(has=page.get_by_role("heading", name="微信按需查工作站", exact=True))
    douyin_memory = panel.locator(".configuration-owner-binding").filter(has=page.get_by_role("heading", name="微信按需查抖音", exact=True))
    expect(douyin_memory.get_by_text("请先绑定微信本人。", exact=True)).to_be_visible()
    expect(cross.get_by_text("请先绑定微信本人。", exact=True)).to_be_visible()
    assert not cross_writes and not douyin_writes
    expect(memory.get_by_role("checkbox", name="memory-a@chatroom", exact=True)).not_to_be_checked()
    assert not memory_writes
    memory.get_by_role("checkbox", name="memory-a@chatroom", exact=True).check()
    memory.get_by_role("button", name="保存工作站查询范围").click()
    expect(memory.get_by_text("工作站查询范围已保存。", exact=True)).to_be_visible()
    assert memory_writes[0]["station_reads"] == ["memory-a@chatroom"]
    message = owner.get_by_label("本人发来的普通文字消息编号")
    message.fill("owner-message")
    page.keyboard.press("Escape")
    page.locator("#app-configuration-open").click()
    nav.get_by_role("button", name="账号与授权", exact=True).click()
    expect(message).to_have_value("owner-message")
    owner.get_by_role("button", name="预览本人身份").click()
    expect(owner.get_by_text("原生发送者 wxid-synthetic-owner · 会话 synthetic-group@chatroom", exact=True)).to_be_visible()
    bind = owner.get_by_role("button", name="绑定本人", exact=True)
    expect(bind).to_be_disabled()
    owner.get_by_role("checkbox", name="确认核对到的发送者就是我本人").check()
    bind.click()
    expect(owner.get_by_text("微信本人绑定已保存。", exact=True)).to_be_visible()
    expect(cross.get_by_text("本人绑定已变化。请载入最新范围，再核对并保存。", exact=True)).to_be_visible()
    cross.get_by_role("button", name="载入最新跨群查询范围").click()
    expect(cross.get_by_text("本人私聊已获回复授权。", exact=True)).to_be_visible()
    cross.get_by_role("group", name="允许本人从这些会话发起").get_by_role("checkbox", name="memory-a@chatroom", exact=True).check()
    cross.get_by_role("group", name="允许查询这些会话的记忆").get_by_role("checkbox", name="memory-b@chatroom", exact=True).check()
    page.keyboard.press("Escape")
    page.locator("#app-configuration-open").click()
    nav.get_by_role("button", name="账号与授权", exact=True).click()
    expect(cross.get_by_role("checkbox", name="memory-b@chatroom", exact=True)).to_be_checked()
    assert not cross_writes
    cross.get_by_role("button", name="保存微信跨群查询范围", exact=True).click()
    expect(cross.get_by_text("微信跨群查询范围已保存。", exact=True)).to_be_visible()
    assert cross_writes[0]["sources"] == ["memory-b@chatroom"] and cross_writes[0]["origins"] == ["memory-a@chatroom"]
    station_memory.get_by_role("button", name="载入最新工作站查询范围").click()
    station_origin = station_memory.get_by_role("checkbox", name="memory-a@chatroom", exact=True)
    expect(station_origin).not_to_be_checked()
    expect(station_memory.get_by_role("checkbox", name="memory-b@chatroom", exact=True)).to_have_count(0)
    station_origin.check()
    station_memory.get_by_role("button", name="保存微信工作站查询范围", exact=True).click()
    expect(station_memory.get_by_text("微信工作站查询范围已保存。", exact=True)).to_be_visible()
    assert station_writes[0]["origins"] == ["memory-a@chatroom"] and len(cross_writes) == 1 and len(memory_writes) == 1
    station_memory.scroll_into_view_if_needed()
    page.screenshot(path=str(output/"wechat-to-station-grants-desktop-small.png"))
    douyin_memory.get_by_role("button", name="载入最新抖音查询范围").click()
    douyin_memory.get_by_role("checkbox", name="memory-a@chatroom", exact=True).check()
    douyin_memory.get_by_role("checkbox", name="本人作品文字", exact=True).check()
    page.keyboard.press("Escape")
    page.locator("#app-configuration-open").click()
    nav.get_by_role("button", name="账号与授权", exact=True).click()
    expect(douyin_memory.get_by_role("checkbox", name="本人作品文字", exact=True)).to_be_checked()
    assert not douyin_writes
    douyin_memory.get_by_role("button", name="保存微信抖音查询范围", exact=True).click()
    expect(douyin_memory.get_by_text("微信抖音查询范围已保存。", exact=True)).to_be_visible()
    assert douyin_writes[0]["app_sources"] == ["works"] and douyin_writes[0]["origins"] == ["memory-a@chatroom"]
    douyin_memory.scroll_into_view_if_needed()
    page.screenshot(path=str(output/"wechat-to-douyin-grants-desktop-small.png"))
    owner.get_by_role("button", name="核验消息发送者").click()
    expect(owner.get_by_text("此消息的原生发送者与已绑定本人一致。", exact=True)).to_be_visible()
    message.fill("member-message")
    owner.get_by_role("button", name="核验消息发送者").click()
    expect(owner.get_by_text("此消息的原生发送者与已绑定本人不一致。", exact=True)).to_be_visible()
    page.screenshot(path=str(output/"wechat-owner-binding-desktop-small.png"))
    cross.scroll_into_view_if_needed()
    page.screenshot(path=str(output/"wechat-cross-memory-grants-desktop-small.png"))
    assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    panel.get_by_label("当前连接").select_option("wechat-synthetic-two")
    expect(owner.get_by_text("尚未绑定微信本人", exact=True)).to_be_visible()
    expect(owner.get_by_text("已保存本人发送者 wxid-synthetic-owner", exact=False)).to_have_count(0)
    expect(memory.get_by_role("checkbox", name="memory-a@chatroom", exact=True)).to_have_count(0)
    expect(cross.get_by_role("checkbox", name="memory-b@chatroom", exact=True)).to_have_count(0)
    expect(station_origin).to_have_count(0)
    expect(douyin_memory.get_by_role("checkbox", name="本人作品文字", exact=True)).to_have_count(0)
    panel.get_by_label("当前连接").select_option("wechat-synthetic-one")
    owner.get_by_role("button", name="解除本人绑定").click()
    expect(owner.get_by_text("微信本人绑定已解除。", exact=True)).to_be_visible()
    expect(cross.get_by_text("请先绑定微信本人。", exact=True)).to_be_visible()
    cross.get_by_role("button", name="撤销全部跨群查询范围", exact=True).click()
    expect(cross.get_by_text("微信跨群查询范围已保存。", exact=True)).to_be_visible()
    assert cross_writes[-1]["sources"] == [] and cross_writes[-1]["origins"] == []
    expect(station_memory.get_by_text("请先绑定微信本人。", exact=True)).to_be_visible()
    station_memory.get_by_role("button", name="撤销全部工作站查询范围", exact=True).click()
    expect(station_memory.get_by_text("微信工作站查询范围已保存。", exact=True)).to_be_visible()
    assert station_writes[-1]["origins"] == [] and len(station_writes) == 2
    douyin_memory.get_by_role("button", name="撤销全部抖音查询范围", exact=True).click()
    expect(douyin_memory.get_by_text("微信抖音查询范围已保存。", exact=True)).to_be_visible()
    assert douyin_writes[-1]["origins"] == [] and douyin_writes[-1]["app_sources"] == [] and len(douyin_writes) == 2
    assert [row["action"] for row in writes] == ["preview", "bind", "check", "check", "revoke"]
    page.unroute(pattern)
    page.unroute(memory_pattern)
    page.unroute(cross_pattern)
    page.unroute(station_pattern)
    page.unroute(douyin_pattern)


def verify_wechat_surf_schedule(page, panel, nav, output):
    schedule = {"account": "one", "revision": 0, "enabled": False, "public_reader_ready": False,
                "settings": {"topics": ["海边故事"], "sources": [
                    {"id": "synthetic", "name": "合成订阅", "type": "rss", "kind": "text", "enabled": True, "url": "https://synthetic.example/feed"}],
                    "interval_minutes": 60, "interval_max_minutes": 150, "daily_limit": 4, "tool_calls": 12, "timezone": "Asia/Shanghai",
                    "quiet_start": "00:00", "quiet_end": "08:00"}}
    reader = {"installed": False, "enabled": False, "runtime_ready": True}
    reads, writes = [], []
    history_reads = []
    history_path = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-one/surf-history(?:/[^?]+)?(?:\?|$)")
    def retained_history(route):
        request = urlparse(route.request.url)
        assert route.request.method == "GET"
        history_reads.append(request.path)
        if request.path.endswith("/synthetic-job"):
            route.fulfill(json={"text": "只在点击后读取的合成正文"})
        else:
            route.fulfill(json={"rows": [{"job": "synthetic-job", "title": "长期合成收藏", "at": "2026-01-02T00:00:00Z",
                "state": "completed", "site": "synthetic.example", "verified_public_read": True, "readable": True,
                "bookmarked": True, "offset": 0, "returned_characters": 10, "total_characters": 10}], "source_version": "synthetic-version"})
    page.route(history_path, retained_history)
    base = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-one/surf(?:-reader)?$")

    def runtime(route):
        request = route.request
        path = urlparse(request.url).path
        target = reader if path.endswith("/surf-reader") else schedule
        if request.method == "GET":
            reads.append(path)
        else:
            assert request.method == "PUT" and request.headers.get("x-csrf-token")
            body = request.post_data_json
            writes.append(body)
            if target is reader:
                if body["action"] == "install":
                    assert body["grant_network"] is True
                    reader.update(installed=True, revision=1)
                else:
                    assert body["action"] == "enable" and body["revision"] == reader["revision"]
                    reader.update(enabled=True, revision=2)
                    schedule["public_reader_ready"] = True
            else:
                assert body["key"] and body["revision"] == schedule["revision"]
                if body["action"] == "save":
                    assert body["settings"]["topics"] == ["隔离测试主题", "第二行"]
                    schedule["settings"] = body["settings"]
                if body["action"] == "resume":
                    assert body["allow_public_reads"] is True
                    schedule["enabled"] = True
                if body["action"] == "pause":
                    schedule["enabled"] = False
                schedule["revision"] += 1
        route.fulfill(json=target)

    page.route(base, runtime)
    page.locator("#app-configuration-open").click()
    expect(nav.get_by_role("button", name="网上冲浪")).to_be_visible()
    assert not reads and not writes
    verify_wechat_owner_binding(page, panel, nav, output)
    nav.get_by_role("button", name="网上冲浪").click()
    live = panel.locator(".configuration-surf-live")
    expect(live.get_by_text("调度暂停", exact=True)).to_be_visible()
    assert len(reads) == 2 and not writes
    textarea = live.get_by_label("兴趣主题（每行一项）")
    textarea.fill("隔离测试主题\n第二行")
    page.keyboard.press("Escape")
    expect(panel).not_to_be_visible()
    page.locator("#app-configuration-open").click()
    nav.get_by_role("button", name="网上冲浪").click()
    expect(textarea).to_have_value("隔离测试主题\n第二行")
    nav.get_by_role("button", name="会话与记忆").click()
    nav.get_by_role("button", name="网上冲浪").click()
    expect(textarea).to_have_value("隔离测试主题\n第二行")
    confirm = live.get_by_role("checkbox", name="授权此连接读取所选公开网页来源")
    install = live.get_by_role("button", name="安装公开网页工具")
    expect(install).to_be_disabled()
    live.get_by_role("button", name="保存并暂停").click()
    expect(live.get_by_text("已保存，调度暂停。", exact=True)).to_be_visible()
    confirm.check()
    install.click()
    live.get_by_role("button", name="启用公开网页工具").click()
    enable = live.get_by_role("button", name="启用冲浪", exact=True)
    expect(enable).to_be_disabled()
    confirm.check()
    enable.click()
    expect(live.get_by_text("已启用，随微信工作器运行", exact=True)).to_be_visible()
    assert not history_reads
    live.get_by_role("button", name="读取本机浏览记录").click()
    expect(live.get_by_text("长期合成收藏 · 已收藏", exact=False)).to_be_visible()
    assert len(history_reads) == 1
    expect(live.get_by_text("只在点击后读取的合成正文", exact=True)).to_have_count(0)
    live.get_by_text("长期合成收藏 · 已收藏", exact=False).click()
    live.get_by_role("button", name="查看已读正文").click()
    expect(live.get_by_text("只在点击后读取的合成正文", exact=True)).to_be_visible()
    assert len(history_reads) == 2
    assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    page.screenshot(path=str(output/"wechat-surf-schedule-desktop-small.png"))
    live.get_by_role("button", name="暂停冲浪").click()
    expect(live.get_by_text("调度暂停", exact=True)).to_be_visible()
    expect(live.get_by_text("只在点击后读取的合成正文", exact=True)).to_have_count(0)
    assert [body["action"] for body in writes] == ["save", "install", "enable", "resume", "pause"]
    page.keyboard.press("Escape")
    page.unroute(base)
    page.unroute(history_path)


def verify_application_configuration(page, output):
    reads = []
    page.on("request", lambda request: reads.append((request.method, urlparse(request.url).path)))
    home_tab(page, "apps")
    page.get_by_role("button", name="管理微信", exact=True).click()
    opener = page.locator("#app-configuration-open")
    start = len(reads)
    opener.click()
    panel = page.locator("#app-configuration")
    expect(panel).to_be_visible()
    nav = panel.get_by_role("navigation", name="应用配置分区")
    expect(nav.get_by_role("button", name="会话与记忆")).to_be_visible()
    expect(nav.get_by_role("button", name="投递与回执")).to_be_visible()
    assert ("GET", "/api/local/apps/wechat/configuration") in reads[start:]
    assert all(method == "GET" for method, _ in reads[start:])
    page.screenshot(path=str(output/"wechat-configuration.png"))
    nav.get_by_role("button", name="会话与记忆").click()
    expect(panel.get_by_text("当前连接没有可展示的配置或资料。")).to_be_visible()
    nav.get_by_role("button", name="网上冲浪").click()
    expect(panel.get_by_text("连接微信或接续旧账号快照后可查看冲浪档案。")).to_be_visible()
    assert not any(path.endswith("/legacy-import/surf") for _, path in reads[start:])
    page.keyboard.press("Escape")
    expect(panel).not_to_be_visible()
    expect(opener).to_be_focused()

    close_application(page)
    wechat = {"id": "wechat", "name": "微信", "registration": "station-archive"}
    page.route("**/api/apps", lambda route: route.fulfill(json={"items": [wechat]}))
    page.route("**/api/apps/wechat/connections", lambda route: route.fulfill(json={"items": [
        {"id": "wechat-synthetic-one", "label": "合成账号一"},
        {"id": "wechat-synthetic-two", "label": "合成账号二"}]}))
    surf_pattern = re.compile(r"/api/apps/wechat/connections/wechat-synthetic-\w+/legacy-import/surf(?:\?|$)")
    surf_reads = []

    def surf_read(route):
        request = urlparse(route.request.url)
        assert route.request.method == "GET"
        surf_reads.append((request.path, parse_qs(request.query)))
        if request.query:
            route.fulfill(json={"status": "available", "sessions": [{
                "at": "2026-01-02T00:00:00Z", "thread": "", "visited_count": 1, "failure_count": 0,
                "state": "not_shared", "candidates": [{"title": "合成收藏", "site": "synthetic.example", "bookmarked": True}],
                "observations": [{"state": "body_read", "site": "synthetic.example", "returned_characters": 10, "total_characters": 15}],
            }]})
        else:
            topic = "次账号主题" if "/wechat-synthetic-two/" in request.path else "海洋故事"
            route.fulfill(json={"schedule": "paused", "history_available": topic == "海洋故事",
                "settings": {"enabled": True, "interval_minutes": 45, "interval_max_minutes": 90,
                             "max_sessions_per_day": 3, "topics": [topic], "target_count": 1, "interaction_count": 0}})

    page.route(surf_pattern, surf_read)
    home_tab(page, "chat")
    home_tab(page, "apps")
    expect(page.get_by_role("button", name="管理微信", exact=True)).to_be_visible()
    page.get_by_role("button", name="管理微信", exact=True).click()
    opener.click()
    expect(nav.get_by_role("button", name="网上冲浪")).to_be_visible()
    assert not surf_reads
    page.set_viewport_size({"width": 1280, "height": 740})
    nav.get_by_role("button", name="网上冲浪").click()
    expect(panel.get_by_text("旧配置只读预览 · 调度保持暂停", exact=True)).to_be_visible()
    expect(panel.get_by_text("海洋故事", exact=True)).to_be_visible()
    assert len(surf_reads) == 1 and not surf_reads[0][1]
    panel.get_by_role("button", name="读取浏览档案", exact=True).click()
    panel.get_by_text("本机独立浏览").click()
    expect(panel.get_by_text("合成收藏 · synthetic.example · 已收藏", exact=True)).to_be_visible()
    expect(panel.get_by_text("历史正文读取 · synthetic.example · 偏移 0 · 10/15 字符", exact=True)).to_be_visible()
    assert surf_reads[-1][1] == {"part": ["history"]}
    assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    page.screenshot(path=str(output/"wechat-surf-archive-desktop-small.png"))
    panel.get_by_label("当前连接").select_option("wechat-synthetic-two")
    expect(panel.get_by_text("次账号主题", exact=True)).to_be_visible()
    expect(panel.get_by_text("合成收藏", exact=False)).to_have_count(0)
    page.keyboard.press("Escape")
    expect(opener).to_be_focused()
    close_application(page)
    wechat["registration"] = "station-native"
    home_tab(page, "chat")
    home_tab(page, "apps")
    page.get_by_role("button", name="管理微信", exact=True).click()
    verify_wechat_surf_schedule(page, panel, nav, output)
    close_application(page)
    page.unroute("**/api/apps")
    page.unroute("**/api/apps/wechat/connections")
    page.unroute(surf_pattern)
    page.set_viewport_size({"width": 1440, "height": 900})

    page.get_by_role("button", name="管理抖音", exact=True).click()
    page.set_viewport_size({"width": 1280, "height": 740})
    opener.click()
    expect(nav.get_by_role("button", name="直播与本机预览")).to_be_visible()
    verify_douyin_read_controls(page, panel, nav)
    verify_douyin_notice(page, panel, nav)
    verify_douyin_memory_permissions(page, panel, nav, output)
    nav.get_by_role("button", name="直播与本机预览").click()
    expect(panel.get_by_role("button", name="打开 OBS 本机预览")).to_be_visible()
    expect(panel.get_by_role("button", name="开始直播", exact=True)).to_have_count(0)
    assert panel.evaluate("node => node.getBoundingClientRect().width < innerWidth && node.getBoundingClientRect().width >= innerWidth - 40")
    assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    page.screenshot(path=str(output/"douyin-configuration-desktop-small.png"))
    panel.get_by_role("button", name="打开 OBS 本机预览").click()
    expect(page.locator("#tool-dialog")).to_be_visible()
    expect(page.locator("#tool-dialog-title")).to_contain_text("OBS")
    page.keyboard.press("Escape")
    expect(page.locator("#tool-dialog")).not_to_be_visible()
    page.set_viewport_size({"width": 1440, "height": 900})

    generic = {"id": "synthetic-configuration", "name": "合成应用", "registration": "synthetic"}
    page.route("**/api/apps", lambda route: route.fulfill(json={"items": [generic]}))
    page.route("**/api/apps/synthetic-configuration/connections", lambda route: route.fulfill(json={"items": []}))
    page.route("**/api/local/apps/synthetic-configuration/configuration", lambda route: route.fulfill(json={
        "id": generic["id"], "registered": True, "sections": [
            {"id": "overview", "title": "概览"}, {"id": "account", "title": "账号与授权"}, {"id": "source", "title": "资料与版本"},
        ],
    }))
    home_tab(page, "apps")
    page.get_by_role("button", name="管理合成应用", exact=True).click()
    opener.click()
    expect(nav.get_by_role("button")).to_have_text(["概览", "账号与授权", "资料与版本"])
    panel.get_by_role("button", name="返回应用").click()
    expect(panel).not_to_be_visible()
    close_application(page)
    page.unroute("**/api/apps")
    page.unroute("**/api/apps/synthetic-configuration/connections")
    page.unroute("**/api/local/apps/synthetic-configuration/configuration")


def open_configuration(page, app, section="概览"):
    open_application(page, app, "概览")
    page.locator("#app-configuration-open").click()
    page.locator("#app-configuration-nav").get_by_role("button", name=section, exact=True).click()


def verify_summary_configuration_boundary(page, output):
    home_tab(page, "apps")
    for app in ("微信", "抖音"):
        close_application(page)
        open_application(page, app, "概览")
        body = page.locator("#app-dialog-body")
        for name in ("概览", "账号与权限", "应用能力", "资料与版本"):
            page.locator("#app-dialog-tabs").get_by_role("button", name=name, exact=True).click()
            expect(body.locator("form,input,textarea,select")).to_have_count(0)
            assert body.locator("button").count() == 1
        page.locator("#app-dialog-tabs").get_by_role("button", name="账号与权限", exact=True).click()
        shortcut = body.get_by_role("button", name="管理账号与授权", exact=True)
        shortcut.click()
        expect(page.locator("#app-configuration")).to_be_visible()
        expect(page.locator("#app-configuration-nav [aria-pressed=true]")).to_have_text("账号与授权")
        expect(body.locator("form,input,textarea,select")).to_have_count(0)
        page.keyboard.press("Escape")
        expect(page.locator("#app-configuration")).not_to_be_visible()
        expect(shortcut).to_be_focused()
        expect(page.locator("#app-dialog-tabs [aria-pressed=true]")).to_have_text("账号与权限")
        page.screenshot(path=str(output / ("wechat-summary.png" if app == "微信" else "douyin-summary.png")))
    close_application(page)


def verify(binary, output):
    output.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='synthetic-layout-',dir=output) as temporary:
        directory=Path(temporary).resolve()
        process,origin=start(binary,directory/'host',directory/'work')
        try:
            with sync_playwright() as playwright:
                browser=playwright.chromium.launch(channel='msedge',headless=True)
                context=browser.new_context(viewport={'width':1440,'height':900})
                page=context.new_page();errors=[]
                page.on('pageerror',lambda error:errors.append(str(error)))
                page.goto(origin)
                page.locator('#login-password').fill('synthetic-home-owner')
                with page.expect_response(lambda response:response.url.endswith('/api/login')) as login:
                    page.locator('#login-submit').click()
                csrf=login.value.json()['csrf']
                page.locator('#initialize-button').click()
                page.locator('#chat-view').wait_for(state='visible')
                initial=fixed_shell(page)
                verify_glass_views(page,output)
                verify_memory_source_selection(page,output)
                verify_application_configuration(page,output)
                verify_summary_configuration_boundary(page,output)
                main=context.request.get(origin+'/api/local/model').json()['persona']
                home_tab(page,'apps');page.locator('#apps-list[aria-busy="false"]').wait_for()
                assert page.get_by_role('button',name='安装源码副本',exact=True).count()==0
                page.screenshot(path=str(output/'applications-desktop.png'))
                for app,text in [('微信','合成微信偏好：喜欢音乐和简洁表达。'),('抖音','合成抖音偏好：喜欢旅行和摄影。')]:
                    open_configuration(page,app)
                    form=page.get_by_role('form',name=app+'子角色',exact=True)
                    form.get_by_role('button',name='编辑',exact=True).click()
                    field=form.get_by_role('textbox',name=app+'子角色设定',exact=True)
                    field.fill(text)
                    form.get_by_role('button',name='保存子角色',exact=True).click()
                    expect(form.get_by_role('button',name='编辑',exact=True)).to_be_visible()
                    result=context.request.get(origin+f'/api/local/apps/{"wechat" if app=="微信" else "douyin"}/persona').json()
                    assert result['persona']==text
                    assert context.request.get(origin+'/api/local/model').json()['persona']==main
                    page.screenshot(path=str(output/f'{"wechat" if app=="微信" else "douyin"}-persona-desktop.png'))
                    close_application(page)
                # A long right-hand settings page must not resize the scene or move the document.
                home_tab(page,'project-tools');page.locator('#project-tools-list[aria-busy="false"]').wait_for()
                fixed_shell(page,initial)
                cards=page.locator('.project-tool-card')
                sizes=cards.evaluate_all('nodes => nodes.map(node => ({w:node.getBoundingClientRect().width,h:node.getBoundingClientRect().height}))')
                assert sizes and all(abs(card[axis]-sizes[0][axis]) < 1 for card in sizes for axis in ('w','h')),sizes
                page.get_by_role('button',name='管理本机语音',exact=True).click()
                expect(page.locator('#tool-dialog')).to_be_visible()
                assert page.locator('#tool-dialog-body').evaluate('node => node.scrollWidth<=node.clientWidth+1')
                page.screenshot(path=str(output/'tool-dialog-desktop.png'))
                page.keyboard.press('Escape')
                expect(page.locator('#tool-dialog')).not_to_be_visible()
                page.locator('#project-tools-view').evaluate('node => node.scrollTop=node.scrollHeight')
                fixed_shell(page,initial)
                page.screenshot(path=str(output/'tools-desktop-scrolled.png'))
                home_tab(page,'settings')
                page.locator('#model-persona').fill('未保存的主角色草稿')
                page.locator('#settings-view').evaluate('node => node.scrollTop=node.scrollHeight')
                fixed_shell(page,initial)
                page.screenshot(path=str(output/'settings-desktop-scrolled.png'))
                home_tab(page,'apps');page.locator('#apps-list[aria-busy="false"]').wait_for()
                open_configuration(page,'微信')
                form=page.get_by_role('form',name='微信子角色',exact=True)
                form.get_by_role('button',name='编辑',exact=True).click()
                field=form.get_by_role('textbox',name='微信子角色设定',exact=True)
                field.fill('暂未保存的应用草稿')
                page.locator('#app-configuration-nav').get_by_role('button',name='资料与版本',exact=True).click()
                page.locator('#app-configuration-nav').get_by_role('button',name='概览',exact=True).click()
                assert field.input_value()=='暂未保存的应用草稿'
                # Force a long form body to verify the dialog scrolls and the fixed header stays usable.
                field.evaluate('node => node.style.minHeight="1000px"')
                header_top=page.locator('#app-configuration .app-dialog-header').bounding_box()['y']
                page.locator('#app-configuration-body').evaluate('node => node.scrollTop=node.scrollHeight')
                assert page.locator('#app-configuration-body').evaluate('node => node.scrollTop')>0
                assert page.locator('#app-configuration .app-dialog-header').bounding_box()['y']==header_top
                fixed_shell(page,initial)
                page.keyboard.press('Escape')
                expect(page.locator('#app-configuration')).not_to_be_visible()
                close_application(page)
                home_tab(page,'settings')
                assert page.locator('#model-persona').input_value()=='未保存的主角色草稿'
                for width,height,theme in [(1024,600,'light'),(1280,800,'light'),(1440,900,'dark')]:
                    close_application(page);page.set_viewport_size({'width':width,'height':height});page.emulate_media(color_scheme=theme)
                    fixed_shell(page)
                    home_tab(page,'apps');page.locator('#apps-list[aria-busy="false"]').wait_for()
                    page.screenshot(path=str(output/f'applications-{width}-{height}-{theme}.png'))
                    open_configuration(page,'抖音')
                    form=page.get_by_role('form',name='抖音子角色',exact=True)
                    form.get_by_role('button',name='编辑',exact=True).click()
                    field=form.get_by_role('textbox',name='抖音子角色设定',exact=True)
                    expect(field).to_have_value('合成抖音偏好：喜欢旅行和摄影。')
                    page.screenshot(path=str(output/f'dialog-{width}-{height}-{theme}.png'))
                    dialog=page.locator('#app-configuration').bounding_box()
                    assert dialog['x']>=0 and dialog['x']+dialog['width']<=width+1
                    assert dialog['y']>=0 and dialog['y']+dialog['height']<=height+1
                    assert not page.locator('#app-configuration-body').evaluate('node => node.scrollWidth>node.clientWidth+1')
                    form.get_by_role('button',name='取消',exact=True).click()
                    close_application(page)
                assert not errors, errors
                browser.close()
        finally:stop(process)
    print(json.dumps({'status':'passed','model_calls':0,'wechat_sends':0,'scenes':['chat and memory palace','shared application details', 'registered application configuration','viewport-fixed scene','independent content scrolling','large app dialog','per-app persona isolation','draft preservation','escape close','desktop and desktop-small light dark']},ensure_ascii=False))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary',required=True,type=Path)
    parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args()
    verify(args.binary.resolve(),args.output.resolve())
