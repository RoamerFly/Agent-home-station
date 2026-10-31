"""Offline subprocess and loopback MCP checks. Never contacts a provider."""
import base64,json,subprocess,sys,tempfile,urllib.request
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def suite(call):
 r=call({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"offline","version":"1"}}});assert r["result"]["serverInfo"]
 r=call({"jsonrpc":"2.0","id":2,"method":"tools/list"});assert len(r["result"]["tools"])==6
 def tool(name,args):
  r=call({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":name,"arguments":args}})
  assert not r["result"]["isError"],r
  return json.loads(r["result"]["content"][0]["text"])
 sample=tool("artifact_sample",{});assert sample["delivery"]=="not_submitted";assert base64.b64decode(sample["media"][0]["data_base64"]).startswith(b"RIFF")
 assert tool("health",{})["status"]=="ready"
 assert tool("echo",{"text":"synthetic"})["text"]=="synthetic"
 assert tool("job_start",{"request_key":"test"})["state"]=="running"
 assert tool("job_start",{"request_key":"test"})["id"]=="test"
 assert tool("job_cancel",{"id":"test"})["state"]=="cancelled"
 assert tool("job_start",{"request_key":"test"})["state"]=="cancelled"
 tool("job_start",{"request_key":"complete"});assert tool("job_status",{"id":"complete"})["state"]=="completed"
 assert tool("job_status",{"id":"unknown"})["state"]=="needs_review"
def stdio(command):
 p=subprocess.Popen(command,cwd=ROOT,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding="utf-8")
 try:
  def call(req):
   p.stdin.write(json.dumps(req)+"\n");p.stdin.flush();return json.loads(p.stdout.readline())
  suite(call)
 finally:
  p.stdin.close()
  try:p.wait(timeout=5)
  except subprocess.TimeoutExpired:p.kill();p.wait()
  p.stdout.close();p.stderr.close()
def main():
 stdio([sys.executable,"templates/plugins/python-stdio/main.py"])
 with tempfile.TemporaryDirectory(prefix="plugin-template-") as tmp:
  exe=Path(tmp)/("sample.exe" if sys.platform=="win32" else "sample")
  subprocess.run(["go","build","-o",str(exe),"./templates/plugins/go-stdio"],cwd=ROOT,check=True)
  stdio([str(exe)])
 p=subprocess.Popen([sys.executable,"templates/plugins/http-mcp/main.py"],cwd=ROOT,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding="utf-8")
 try:
  url=json.loads(p.stdout.readline())["url"]
  def call(req):
   request=urllib.request.Request(url,data=json.dumps(req).encode(),headers={"Content-Type":"application/json"})
   with urllib.request.urlopen(request,timeout=5) as response:return json.load(response)
  suite(call)
 finally:p.terminate();p.wait(timeout=5);p.stdout.close();p.stderr.close()
 print(json.dumps({"templates":3,"status":"passed","paid_calls":0,"wechat_sends":0}))
if __name__=="__main__":main()
