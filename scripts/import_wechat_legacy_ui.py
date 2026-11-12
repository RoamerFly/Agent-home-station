"""Import the user-approved WeChat canvas baseline, with host-only adaptations.

This is a developer migration command, never run by installed applications.
"""
import re
import subprocess
from pathlib import Path

BASELINE = "4e230477ebe42cdb16adba87ae7c19055c93b2dc"
ROOT = Path(__file__).resolve().parents[1]
TARGET = ROOT / "internal/wechatui/static"


def source(name):
    return subprocess.check_output(
        ["git", "show", f"{BASELINE}:internal/admin/static/{name}"], cwd=ROOT
    ).decode("utf-8")


def desktop_css(css):
    # The application is desktop-only. Keep the old desktop editor rules.
    pattern = re.compile(r"@media[^{}]*max-width\s*:\s*(\d+)px[^{}]*\{")
    for match in reversed(list(pattern.finditer(css))):
        if int(match[1]) > 900:
            continue
        end, depth = match.end(), 1
        while depth:
            depth += (css[end] == "{") - (css[end] == "}")
            end += 1
        css = css[:match.start()] + css[end:]
    return css


def main():
    TARGET.mkdir(parents=True, exist_ok=True)
    for name in ["rounds.js", "dialogs.js", "flow-model.js", "flow-default.json"]:
        content = source(name)
        if name == "flow-model.js":
            content = content.replace("['plugins','mcp','mobile','permissions']", "['plugins','mcp','permissions']")
        (TARGET / name).write_text(content, encoding="utf-8")
    (TARGET / "workbench.css").write_text(desktop_css(source("workbench.css")), encoding="utf-8")
    js = source("workbench.js")
    js = js.replace("const VIEW_KEY='wechat_agent_canvas_view_v2',DRAFT_KEY='wechat_agent_canvas_draft_v2';",
                    "const scope=window.WechatAccountContext.id;const VIEW_KEY='wechat_agent_canvas_view_v2:'+scope,DRAFT_KEY='wechat_agent_canvas_draft_v2:'+scope;")
    js = js.replace("view:matchMedia('(max-width:600px)').matches?'list':'canvas'", "view:'canvas'")
    js = js.replace("'/assets/flow-default.json'", "'/assets/wechat/flow-default.json'")
    start, end = js.index('observe:('), js.index(',model:')
    js = js[:start] + "observe:'工作站公共媒体理解'" + js[end:]
    js = js.replace("memory:(window.WechatAdmin.getScopes()?.length||0)+' 个会话'", "memory:'按需读取会话记忆'")
    js = js.replace("wake:cfg.companion?.permissions?.proactive?'主动权限开启':'主动权限关闭'", "wake:'按当前授权安排续办'")
    start = js.index(' function showPage(id){')
    end = js.index('\n function inspect(', start)
    js = js[:start] + """ function showPage(id){
  restore();const body=$('#flow-inspector-body');body.replaceChildren();
  const button=el('button','primary','打开'+(pages[id]||'相关功能'));button.type='button';
  button.addEventListener('click',()=>window.WechatApplicationHost.openSection(id));
  body.append(el('p','hint','在当前微信应用或工作站公共工具中管理这项能力。'),button);
  $('#flow-config-page').value=id;
 }
""" + js[end:]
    (TARGET / "workbench.js").write_text(js, encoding="utf-8")
    html = source("index.html")
    section = re.search(r'<section[^>]+id="workbench"[^>]*>[\s\S]*?</section>', html)[0]
    section = section.replace('class="page"', 'class="page active-page"')
    symbols = re.search(r'<svg\b[\s\S]*?</svg>', html)[0]
    shell = f'''<!doctype html>
<html lang="zh-CN" data-theme="dark"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>微信流程与状态</title>
<link rel="stylesheet" href="/assets/wechat/bridge.css">
<link rel="stylesheet" href="/assets/wechat/workbench.css">
<script defer src="/assets/wechat/bridge.js"></script>
<script defer src="/assets/wechat/flow-model.js"></script>
<script defer src="/assets/wechat/rounds.js"></script>
<script defer src="/assets/wechat/dialogs.js"></script>
<script defer src="/assets/wechat/workbench.js"></script>
<script defer src="/assets/wechat/start.js"></script>
</head><body>{symbols}<main class="content">
<div id="notice" hidden role="status"></div><div id="unsaved-bar" hidden></div>
{section}</main></body></html>'''
    (TARGET / "workbench.html").write_text(shell, encoding="utf-8")


if __name__ == "__main__":
    main()
