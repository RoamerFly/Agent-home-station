"""Exercise only the owned local web panel; no credentials appear in output.
Media sends are limited to the test account's File Transfer Assistant and use a
durable pre-send marker. Re-running watches the same task rather than resending.
"""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import time
import urllib.request
import uuid

parser=argparse.ArgumentParser()
parser.add_argument("mode",choices=["state","image","file","voice","video"])
args=parser.parse_args()
root=Path("/home/fly/.local/share/wechat-bot-native-research-20261024")
workspace=Path(__file__).resolve().parents[2]
base="http://127.0.0.1:8081"
jar=http.cookiejar.CookieJar()
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
def request(path,data=None,raw=None,content_type="application/json"):
    headers={"Origin":base}
    if "csrf" in globals(): headers["X-CSRF-Token"]=csrf
    if data is not None:
        raw=json.dumps(data).encode()
    if raw is not None: headers["Content-Type"]=content_type
    req=urllib.request.Request(base+path,data=raw,headers=headers)
    with opener.open(req,timeout=90) as response:
        return json.load(response)
csrf=request("/api/login",{"password":(root/"admin/admin-token").read_text().strip()})["csrf"]
state=request("/api/tools/state")
print(json.dumps({"mediaKinds":state["capabilities"]["mediaKinds"],"filehelper_allowed":any(r["id"]=="filehelper" for r in state["recipients"]),"limits":state["limits"]}),flush=True)
if args.mode=="state":
    raise SystemExit(0)
assert any(r["id"]=="filehelper" for r in state["recipients"])
kind=args.mode
marker=root/("admin/web-"+kind+"-validation.json")
if marker.exists():
    record=json.loads(marker.read_text())
else:
    if kind=="image":
        source=workspace/"internal/admin/static/whale-girl.png"
        mime="image/png"
    else:
        import subprocess
        folder=root/"admin/validation-media"
        folder.mkdir(mode=0o700,exist_ok=True)
        source=folder/{"file":"background-send-test.txt","voice":"background-voice-test.wav","video":"background-video-test.mp4"}[kind]
        mime={"file":"text/plain","voice":"audio/wav","video":"video/mp4"}[kind]
        if not source.exists():
            if kind=="file":
                source.write_text("WeChat background file send test. 2026-10-24.\n",encoding="utf8")
            elif kind=="voice":
                subprocess.run(["ffmpeg","-nostdin","-loglevel","error","-f","lavfi","-i","sine=frequency=660:duration=1","-ar","24000","-ac","1",str(source)],check=True)
            else:
                subprocess.run(["ffmpeg","-nostdin","-loglevel","error","-f","lavfi","-i","testsrc2=size=320x240:rate=10:duration=1","-c:v","libx264","-pix_fmt","yuv420p",str(source)],check=True)
            source.chmod(0o600)
    data=source.read_bytes()
    assert len(data)<state["limits"][kind]
    boundary="image-test-"+uuid.uuid4().hex
    body=("--"+boundary+"\r\nContent-Disposition: form-data; name=\"kind\"\r\n\r\n"+kind+"\r\n--"+boundary+"\r\nContent-Disposition: form-data; name=\"file\"; filename=\""+source.name+"\"\r\nContent-Type: "+mime+"\r\n\r\n").encode()+data+("\r\n--"+boundary+"--\r\n").encode()
    uploaded=request("/api/tools/upload",raw=body,content_type="multipart/form-data; boundary="+boundary)
    record={"source_id":uploaded["id"],"key":"web-"+kind+"-check-"+uuid.uuid4().hex,"recipient":"filehelper","created":int(time.time()),"status":None}
    with marker.open("x") as file:
        os.chmod(marker,0o600)
        json.dump(record,file);file.flush();os.fsync(file.fileno())
    # Marker is persisted before the first and only send submission.
    record["status"]=request("/api/tools/send",{"thread_id":"filehelper","kind":kind,"text":"","source_id":uploaded["id"],"idempotency_key":record["key"]})
for attempt in range(60):
    status=request("/api/tools/lookup?key="+record["key"])
    record["status"]=status
    pending=marker.with_suffix(".pending")
    with pending.open("w") as file:
        os.chmod(pending,0o600);json.dump(record,file);file.flush();os.fsync(file.fileno())
    pending.replace(marker)
    if status["state"] in ["accepted","failed","cancelled","needs_review"]:
        break
    if attempt%5==0: print(json.dumps({"state":status["state"],"phase":status.get("phase")}),flush=True)
    time.sleep(2)
print(json.dumps({"recipient":"filehelper","kind":kind,"status":record["status"]}),flush=True)
evidence=workspace/("research/evidence/web-"+kind+"-validation.json")
evidence.write_text(json.dumps({"validated_via":"web-admin-api","recipient":"own file transfer assistant","phone_display_confirmed":False,"send_attempts_by_helper":1,"status":record["status"]},ensure_ascii=False,indent=2),encoding="utf8")

