"""Synthetic MCP template. No provider calls, credentials or WeChat access."""
import json, sys
TOOLS = [
 {"name":"artifact_sample","description":"Return synthetic silent WAV bytes; not a delivered voice","inputSchema":{"type":"object"}},
 {"name":"health","description":"Synthetic health check","inputSchema":{"type":"object"}},
 {"name":"echo","description":"Echo synthetic text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}},
 {"name":"job_start","description":"Prepare an offline sample job","inputSchema":{"type":"object","properties":{"request_key":{"type":"string"}},"required":["request_key"]}},
 {"name":"job_status","description":"Read sample job state","inputSchema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}},
 {"name":"job_cancel","description":"Cancel a sample job","inputSchema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}
]
# The educational transport keeps jobs in memory; a restart explicitly returns
# needs_review. Production plugins must persist provider IDs via host job storage.
jobs = {}
def call(name,args):
 if name == "artifact_sample": return {"media":[{"mime_type":"audio/wav","filename":"synthetic.wav","data_base64":"UklGRsQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YaAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}],"synthetic":True,"delivery":"not_submitted"}
 if name == "health": return {"status":"ready","synthetic":True}
 if name == "echo": return {"text":str(args.get("text",""))}
 if name == "job_start":
  key=args.get("request_key")
  if not isinstance(key,str) or not key or len(key)>128: raise ValueError("request_key required")
  if key not in jobs: jobs[key]={"id":key,"state":"running","synthetic":True}
  return jobs[key]
 if name in ("job_status","job_cancel"):
  key=args.get("id")
  if key not in jobs: return {"id":key,"state":"needs_review","reason":"sample_state_not_persisted"}
  if name=="job_cancel":jobs[key]["state"]="cancelled"
  elif jobs[key]["state"]=="running":jobs[key]["state"]="completed"
  return jobs[key]
 raise ValueError("unknown tool")
def handle(req):
 if "id" not in req:return None
 method=req.get("method")
 if method=="initialize":result={"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic-python-template","version":"0.1.0"}}
 elif method=="tools/list":result={"tools":TOOLS}
 elif method=="ping":result={}
 elif method=="tools/call":
  try:
   value=call(req["params"]["name"],req["params"].get("arguments",{}))
   result={"content":[{"type":"text","text":json.dumps(value)}],"isError":False}
  except (ValueError,KeyError,TypeError) as e:result={"content":[{"type":"text","text":str(e)}],"isError":True}
 else:return {"jsonrpc":"2.0","id":req["id"],"error":{"code":-32601,"message":"unknown method"}}
 return {"jsonrpc":"2.0","id":req["id"],"result":result}
if __name__=="__main__":
 for line in sys.stdin:
  try:
   response=handle(json.loads(line))
   if response is not None:print(json.dumps(response),flush=True)
  except (ValueError,TypeError):print(json.dumps({"jsonrpc":"2.0","id":None,"error":{"code":-32700,"message":"invalid JSON"}}),flush=True)
