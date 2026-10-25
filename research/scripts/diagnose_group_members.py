import json,urllib.request,urllib.error
from pathlib import Path
r=Path("/home/fly/.local/share/wechat-bot-native-research-20261024")
c=json.loads((r/"admin/config.json").read_text())
g=c["group_rules"][0]["id"]
u="http://127.0.0.1:6174/v1/accounts/background-test-20261024/groups/"+g+"/members?limit=50"
op=urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
 with op.open(urllib.request.Request(u,headers={"Authorization":"Bearer "+(r/"api-token").read_text().strip()}),timeout=20)as x:
  data=json.load(x);print(json.dumps({"ok":True,"count":len(data.get("items",[]))}))
except urllib.error.HTTPError as e:
 data=json.load(e);print(json.dumps({"status":e.code,"error":data.get("error",{})}))
