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
    page.set_viewport_size({"width": 375, "height": 740})
    fixed_shell(page)
    expect(page.locator("#memory-detail")).to_contain_text("周日一起画明信片。")
    page.screenshot(path=str(output/"memory-narrow.png"))
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
    login = {"revision": 7, "state": "saved", "active": False, "available": True, "notice_bound": False}

    def notice(route):
        data = route.request.post_data_json
        action = data["action"]
        observed.append(action)
        assert data["revision"] == login["revision"]
        if action in ("bind", "revoke"):
            assert "allow_mark_read" not in data
            login["notice_bound"] = action == "bind"
            login["revision"] += 1
            route.fulfill(json={"revision": login["revision"], "notice_bound": login["notice_bound"]})
        elif action == "prepare":
            assert data["allow_mark_read"] is True
            route.fulfill(json={"ticket": "synthetic-single-use"})
        else:
            assert action == "read" and data["allow_mark_read"] is True and data["ticket"] == "synthetic-single-use"
            route.fulfill(json={"items": [{"id": "1234", "type": 2, "work_id": "5678"}], "has_more": True})

    page.route(status_path, lambda route: route.fulfill(json=login))
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
    read.click()
    expect(panel.get_by_text("通知 1234 · 平台类型 2", exact=True)).to_be_visible()
    expect(panel.get_by_text("本次返回 1 条通知。平台报告还有后续通知。")).to_be_visible()
    assert observed == ["bind", "prepare", "read"]
    panel.get_by_role("button", name="撤销通知绑定", exact=True).click()
    expect(bind).to_be_visible()
    expect(panel.get_by_text("通知 1234 · 平台类型 2", exact=True)).to_have_count(0)
    assert observed[-1] == "revoke"
    page.unroute(status_path)
    page.unroute(notice_path)


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
    page.keyboard.press("Escape")
    expect(panel).not_to_be_visible()
    expect(opener).to_be_focused()

    page.get_by_role("button", name="管理抖音", exact=True).click()
    page.set_viewport_size({"width": 375, "height": 740})
    opener.click()
    expect(nav.get_by_role("button", name="直播与本机预览")).to_be_visible()
    verify_douyin_read_controls(page, panel, nav)
    verify_douyin_notice(page, panel, nav)
    nav.get_by_role("button", name="直播与本机预览").click()
    expect(panel.get_by_role("button", name="打开 OBS 本机预览")).to_be_visible()
    expect(panel.get_by_role("button", name="开始直播", exact=True)).to_have_count(0)
    assert panel.evaluate("node => node.getBoundingClientRect().width === innerWidth")
    assert panel.locator("#app-configuration-body").evaluate("node => node.scrollWidth <= node.clientWidth + 1")
    page.screenshot(path=str(output/"douyin-configuration-narrow.png"))
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
                verify_application_configuration(page,output)
                main=context.request.get(origin+'/api/local/model').json()['persona']
                home_tab(page,'apps');page.locator('#apps-list[aria-busy="false"]').wait_for()
                assert page.get_by_role('button',name='安装接入版本',exact=True).count()==0
                page.screenshot(path=str(output/'applications-desktop.png'))
                for app,text in [('微信','合成微信偏好：喜欢音乐和简洁表达。'),('抖音','合成抖音偏好：喜欢旅行和摄影。')]:
                    open_application(page,app,'概览')
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
                open_application(page,'微信','概览')
                form=page.get_by_role('form',name='微信子角色',exact=True)
                form.get_by_role('button',name='编辑',exact=True).click()
                field=form.get_by_role('textbox',name='微信子角色设定',exact=True)
                field.fill('暂未保存的应用草稿')
                page.locator('#app-dialog-tabs').get_by_role('button',name='资料与版本',exact=True).click()
                page.locator('#app-dialog-tabs').get_by_role('button',name='概览',exact=True).click()
                assert field.input_value()=='暂未保存的应用草稿'
                # Force a long form body to verify the dialog scrolls and the fixed header stays usable.
                field.evaluate('node => node.style.minHeight="1000px"')
                header_top=page.locator('#app-dialog .app-dialog-header').bounding_box()['y']
                page.locator('#app-dialog-body').evaluate('node => node.scrollTop=node.scrollHeight')
                assert page.locator('#app-dialog-body').evaluate('node => node.scrollTop')>0
                assert page.locator('#app-dialog .app-dialog-header').bounding_box()['y']==header_top
                fixed_shell(page,initial)
                page.keyboard.press('Escape')
                expect(page.locator('#app-dialog')).not_to_be_visible()
                home_tab(page,'settings')
                assert page.locator('#model-persona').input_value()=='未保存的主角色草稿'
                for width,height,theme in [(1024,600,'light'),(375,740,'light'),(375,600,'dark')]:
                    close_application(page);page.set_viewport_size({'width':width,'height':height});page.emulate_media(color_scheme=theme)
                    fixed_shell(page)
                    home_tab(page,'apps');page.locator('#apps-list[aria-busy="false"]').wait_for()
                    page.screenshot(path=str(output/f'applications-{width}-{height}-{theme}.png'))
                    open_application(page,'抖音','概览')
                    form=page.get_by_role('form',name='抖音子角色',exact=True)
                    form.get_by_role('button',name='编辑',exact=True).click()
                    field=form.get_by_role('textbox',name='抖音子角色设定',exact=True)
                    expect(field).to_have_value('合成抖音偏好：喜欢旅行和摄影。')
                    page.screenshot(path=str(output/f'dialog-{width}-{height}-{theme}.png'))
                    dialog=page.locator('#app-dialog').bounding_box()
                    assert dialog['x']>=0 and dialog['x']+dialog['width']<=width+1
                    assert dialog['y']>=0 and dialog['y']+dialog['height']<=height+1
                    assert not page.locator('#app-dialog-body').evaluate('node => node.scrollWidth>node.clientWidth+1')
                    form.get_by_role('button',name='取消',exact=True).click()
                    close_application(page)
                assert not errors, errors
                browser.close()
        finally:stop(process)
    print(json.dumps({'status':'passed','model_calls':0,'wechat_sends':0,'scenes':['chat and memory palace','shared application details', 'registered application configuration','viewport-fixed scene','independent content scrolling','large app dialog','per-app persona isolation','draft preservation','escape close','desktop and narrow light dark']},ensure_ascii=False))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary',required=True,type=Path)
    parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args()
    verify(args.binary.resolve(),args.output.resolve())
