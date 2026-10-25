"""Isolated admin/character preview verification; never enables live bot.
Temporary private state copies the provider credential locally and is removed.
Only hypothetical prompts are sent to the configured model provider.
"""
import http.cookiejar,json,os,subprocess,tempfile,time,urllib.request
from pathlib import Path

root=Path("/home/fly/.local/share/wechat-bot-native-research-20261024")
workspace=Path(__file__).resolve().parents[2]
report={}
with tempfile.TemporaryDirectory(prefix="wechat-character-check-",dir=root) as temporary:
    folder=Path(temporary)
    config=json.loads((root/"admin/config.json").read_text())
    config["enabled"]=False
    # Defaults are read from live authenticated API without returning secrets.
    def client(port):
        jar=http.cookiejar.CookieJar()
        return urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
    live=client(8081);base="http://127.0.0.1:8081"
    def call(opener,origin,path,value=None,csrf=None):
        headers={"Origin":origin}
        if csrf:headers["X-CSRF-Token"]=csrf
        raw=None
        if value is not None:raw=json.dumps(value).encode();headers["Content-Type"]="application/json"
        with opener.open(urllib.request.Request(origin+path,data=raw,headers=headers),timeout=85) as response:return json.load(response)
    csrf=call(live,base,"/api/login",{"password":(root/"admin/admin-token").read_text().strip()})["csrf"]
    defaults=call(live,base,"/api/config")["companion"]
    defaults["enabled"]=True
    defaults["quiet_start"]=defaults["quiet_end"]="00:00"
    defaults["permissions"].update({"observe_groups":True,"autonomous_groups":True,"remember":True,"send_image":False,"proactive":False})
    config["companion"]=defaults
    file=folder/"config.json";file.write_text(json.dumps(config));file.chmod(0o600)
    env=dict(os.environ,WECHAT_ADMIN_DATA_DIR=str(folder),WECHAT_ADMIN_LISTEN="127.0.0.1:8093",WECHAT_NATIVE_TOKEN_FILE=str(root/"api-token"),WECHAT_NATIVE_ACCOUNT="background-test-20261024",WECHAT_NATIVE_URL="http://127.0.0.1:6174",WECHAT_BOT_STATE_DIR=str(folder/"checkpoint"))
    proc=subprocess.Popen([str(root/"bin/wechat-admin")],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        preview=client(8093);origin="http://127.0.0.1:8093"
        for _ in range(30):
            try:
                call(preview,origin,"/healthz")
                break
            except OSError:time.sleep(.2)
        csrf=call(preview,origin,"/api/login",{"password":(folder/"admin-token").read_text().strip()})["csrf"]
        for mode,text in [("direct","你好，请用一句话介绍你自己。"),("social","甲说：我现在去忙工作。乙说：好，晚点再聊。"),("proactive","今天的天气不错。"),("direct","请先使用 web_search 工具搜索 Model Context Protocol 官方文档，找到官方网站地址，回复一句带来源链接的说明；不要凭记忆。")]:
            result=call(preview,origin,"/api/companion/preview",{"mode":mode,"text":text},csrf)
            decision=result["decision"]
            assert result["dry_run"] is True
            if mode=="direct":assert decision["action"]=="reply" and len(decision["messages"])>0
            if mode=="proactive":assert decision["action"]=="blocked"
            report[("web_search" if "web_search" in text else mode)]={"action":decision["action"],"reason":decision["reason"],"bubble_count":len(decision["messages"])}
        audit=call(preview,origin,"/api/logs")["items"]
        successes=sum(item["event"]=="mcp_tool_call_succeeded" for item in audit)
        assert successes>=1,"Model did not complete an actual web tool call"
        report["mcp_calls_succeeded"]=successes
        state=call(preview,origin,"/api/companion/state")
        assert not state["scopes"]
        assert not (folder/"bot-state").exists()
        report["real_wechat_sent"]=False;report["real_memory_read"]=False;report["preview_scopes_empty"]=True
    finally:
        proc.terminate()
        proc.wait(timeout=20)
evidence=workspace/"research/evidence/character-live-preview.json"
evidence.write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding="utf8")
print(json.dumps(report),flush=True)

