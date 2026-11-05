"""Loopback-only synthetic HTTP MCP adapter, no external API access."""
import importlib.util,json
from pathlib import Path
from http.server import BaseHTTPRequestHandler,HTTPServer
spec=importlib.util.spec_from_file_location("sample",Path(__file__).parents[1]/"python-stdio/main.py")
sample=importlib.util.module_from_spec(spec);spec.loader.exec_module(sample)
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  if self.path!="/mcp":self.send_error(404);return
  size=int(self.headers.get("Content-Length","0"))
  if not 0<size<=1048576:self.send_error(413);return
  try:result=sample.handle(json.loads(self.rfile.read(size)))
  except (ValueError,TypeError):self.send_error(400);return
  if result is None:self.send_response(202);self.end_headers();return
  body=json.dumps(result).encode();self.send_response(200);self.send_header("Content-Type","application/json");self.send_header("Content-Length",str(len(body)));self.end_headers();self.wfile.write(body)
if __name__=="__main__":
 server=HTTPServer(("127.0.0.1",0),Handler)
 print(json.dumps({"url":"http://127.0.0.1:%d/mcp"%server.server_port}),flush=True)
 server.serve_forever()
