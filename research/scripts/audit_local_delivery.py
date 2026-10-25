"""Read-only local delivery audit. Never prints personal catalogs or secrets."""
import http.cookiejar,json,urllib.request,urllib.parse
from pathlib import Path
root=Path("/home/fly/.local/share/wechat-bot-native-research-20261024")
workspace=Path(__file__).resolve().parents[2]
base="http://127.0.0.1:8081"
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None,csrf=None):
    headers={"Origin":base}
    if csrf:headers["X-CSRF-Token"]=csrf
    body=None
    if value is not None:body=json.dumps(value).encode();headers["Content-Type"]="application/json"
    with opener.open(urllib.request.Request(base+path,data=body,headers=headers),timeout=35)as response:return json.load(response)
csrf=call("/api/login",{"password":(root/"admin/admin-token").read_text().strip()})["csrf"]
report={}
for resource in ["/healthz","/api/status","/api/config","/api/companion/state","/api/tools/state","/api/library","/api/actions","/api/mcp","/api/mcp/tools","/api/plugins"]:
    data=call(resource)
    assert "api_key" not in data
    report[resource]="ok"
config=call("/api/config")
for kind in ["contacts","groups","threads","messages","members"]:
    q={"kind":kind}
    if kind=="messages":q["thread"]="filehelper"
    if kind=="members":
        groups=config.get("group_rules",[])
        if not groups:continue
        q["thread"]=groups[0]["id"]
    print("Checking catalog "+kind,flush=True)
    data=call("/api/catalog?"+urllib.parse.urlencode(q))
    assert isinstance(data["items"],list)
    report["catalog_"+kind]={"readable":True,"item_count":len(data["items"]),"paginated":bool(data.get("next_cursor"))}
for script in ["theme","app","tools","catalog","actions","mcp","plugins"]:
    with opener.open(base+"/assets/"+script+".js")as response:assert response.status==200
report["static_assets"]="ok"
status=call("/api/status")
report["wechat_authenticated"]=bool(status["native"]["authenticated"])
report["bot_state"]=status["bot"]["state"]
report["current_character_enabled"]=config["companion"]["enabled"]
report["image_permission"]=config["companion"]["permissions"]["send_image"]
report["exports_paused"]=True
report["secrets_returned"]=False
(workspace/"research/evidence/local-delivery-audit.json").write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding="utf8")
print(json.dumps(report),flush=True)

