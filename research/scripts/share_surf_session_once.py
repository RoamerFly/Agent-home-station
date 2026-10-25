"""Owner-requested one-off share of an already selected surf video. WSL only.
Requires --session from the operator; never selects a recipient from model text.
Keeps a private no-replay claim and polls the same native send only.
"""
import argparse, hashlib, json, os, time
from pathlib import Path
import urllib.request, urllib.error, urllib.parse
parser=argparse.ArgumentParser(); parser.add_argument("--session",required=True); args=parser.parse_args()
root=Path("/home/fly/.local/share/wechat-bot-native-research-20261024")
admin=root/"admin"
history=json.loads((admin/"surf-history.json").read_text())
session=next(x for x in history["sessions"] if x["id"]==args.session)
assert session["state"] not in ("shared","prepared"), "Already shared or active; no new send"
chosen=next(x for x in session["candidates"] if x["id"]==session.get("selected"))
assert chosen["kind"]=="video", "This owner operation shares the selected video card only"
canon=lambda x:x.rstrip("/")
assert any(canon(x)==canon(chosen["url"]) for x in session.get("visited",[])), "No verified reading"
config=json.loads((admin/"config.json").read_text())
rule=next(x for x in config["group_rules"] if x["id"]==session["thread"])
material="\0".join(("manual-surf-v1",session["id"],chosen["id"],session["thread"]))
key="manual-surf-"+hashlib.sha256(material.encode()).hexdigest()[:40]
claims=admin/"manual-surf-claims"; claims.mkdir(mode=0o700,exist_ok=True)
claim=claims/(key+".json")
first=False
try:
 fd=os.open(claim,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
 with os.fdopen(fd,"w") as f:json.dump({"session_id":session["id"],"candidate_id":chosen["id"],"thread":session["thread"],"key":key,"state":"claimed"},f)
 first=True
except FileExistsError:pass
account="background-test-20261024"
base="http://127.0.0.1:6174/v1/accounts/"+account+"/sends"
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
headers={"Authorization":"Bearer "+(root/"api-token").read_text().strip(),"Content-Type":"application/json","Idempotency-Key":key}
def request(url,body=None):
 req=urllib.request.Request(url,headers=headers,data=None if body is None else json.dumps(body,ensure_ascii=False).encode())
 with client.open(req,timeout=20) as r:return json.load(r)
try:status=request(base+"/by-idempotency-key?key="+urllib.parse.quote(key))
except urllib.error.HTTPError as e:
 if e.code!=404:raise
 assert first,"Uncertain earlier operation; will not resubmit"
 seconds=chosen.get("duration_ms",0)//1000
 status=request(base,{"threadId":session["thread"],"kind":"share","title":chosen["title"],"url":chosen["url"],"description":f"点击打开 B 站播放页（{seconds//60}分{seconds%60:02d}秒）"})
for _ in range(30):
 if status.get("state") in ("accepted","failed","rejected","needs_review","unknown","cancelled"):break
 time.sleep(1);status=request(base+"/"+urllib.parse.quote(status["id"]))
receipt=status.get("serverId") or status.get("serverMessageId")
assert status.get("threadId",status.get("destination"))==session["thread"],"Destination mismatch"
if status.get("state")=="accepted":assert receipt and str(receipt).isdigit() and int(receipt)>0,"Receipt missing"
record={"session_id":session["id"],"candidate_id":chosen["id"],"thread":session["thread"],"key":key,"status":status}
claim.write_text(json.dumps(record,ensure_ascii=False),encoding="utf-8");claim.chmod(0o600)
print(json.dumps({"group":rule["name"],"title":chosen["title"],"state":status.get("state"),"has_receipt":bool(receipt),"key":key,"send_id":status.get("id"),"resubmitted":False},ensure_ascii=False))
